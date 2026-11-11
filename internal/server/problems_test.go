package server

import (
	"path/filepath"
	"testing"
	"time"

	"sentra/internal/proto"
)

func testStore(t *testing.T) *Store {
	st, err := OpenStore(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func push(t *testing.T, st *Store, e *Engine, id string, at time.Time, cpu float64, rootPct float64) {
	m := summarize([]proto.Sample{
		{Name: proto.MCPUUsage, Value: cpu},
		{Name: proto.MMemUsedPct, Value: 40},
		{Name: proto.MFsUsedPct, Labels: map[string]string{"mount": "/"}, Value: rootPct},
		{Name: proto.MFsFree, Labels: map[string]string{"mount": "/"}, Value: 50 << 30},
		{Name: proto.MFsTotal, Labels: map[string]string{"mount": "/"}, Value: 100 << 30},
	})
	if err := st.TouchHost(id, at.Unix(), m, nil); err != nil {
		t.Fatal(err)
	}
	h, _ := st.Host(id)
	e.Eval(h, at)
}

func openKeys(st *Store, id string) map[string]int {
	ps, _ := st.OpenProblemsForHost(id)
	out := map[string]int{}
	for _, p := range ps {
		out[p.Key] = p.Severity
	}
	return out
}

func TestEngineCPUHoldAndHysteresis(t *testing.T) {
	st := testStore(t)
	e := NewEngine(st)
	id, _, _ := st.RegisterHost(proto.Inventory{Hostname: "srv", MachineID: "m1"})
	t0 := time.Date(2026, 11, 11, 10, 0, 0, 0, time.Local)

	// 9 минут высокой нагрузки — ещё не проблема
	for i := 0; i <= 18; i++ {
		push(t, st, e, id, t0.Add(time.Duration(i)*30*time.Second), 97, 40)
	}
	if k := openKeys(st, id); len(k) != 0 {
		t.Fatalf("рано открыли проблему: %v", k)
	}
	// 10 минут — авария
	push(t, st, e, id, t0.Add(10*time.Minute), 97, 40)
	if k := openKeys(st, id); k["cpu"] != 2 {
		t.Fatalf("ждали аварию CPU, есть %v", k)
	}
	// 83% — ниже порога, но в пределах гистерезиса: остаётся «внимание»
	push(t, st, e, id, t0.Add(11*time.Minute), 83, 40)
	if k := openKeys(st, id); k["cpu"] != 1 {
		t.Fatalf("ждали внимание по гистерезису, есть %v", k)
	}
	// 50% — закрыта
	push(t, st, e, id, t0.Add(12*time.Minute), 50, 40)
	if k := openKeys(st, id); len(k) != 0 {
		t.Fatalf("проблема должна закрыться: %v", k)
	}
	ps, _ := st.ProblemsSince(0)
	if len(ps) != 1 || ps[0].MaxSeverity != 2 || ps[0].ClosedAt == nil {
		t.Fatalf("история: %+v", ps)
	}
}

func TestEngineDiskOfflineMaintenance(t *testing.T) {
	st := testStore(t)
	e := NewEngine(st)
	id, _, _ := st.RegisterHost(proto.Inventory{Hostname: "srv", MachineID: "m2"})
	t0 := time.Now().Add(-time.Hour)

	push(t, st, e, id, t0, 10, 91)
	if k := openKeys(st, id); k["disk:/"] != 1 {
		t.Fatalf("диск 91%% — внимание сразу, есть %v", k)
	}
	// агент замолчал: через 4 минуты — «нет данных», диск не трогаем
	h, _ := st.Host(id)
	e.Eval(h, t0.Add(4*time.Minute))
	if k := openKeys(st, id); k["offline"] != 2 || k["disk:/"] != 1 {
		t.Fatalf("ждали offline + диск, есть %v", k)
	}
	// агент вернулся, диск почищен
	push(t, st, e, id, t0.Add(10*time.Minute), 10, 50)
	if k := openKeys(st, id); len(k) != 0 {
		t.Fatalf("всё должно закрыться: %v", k)
	}
	// обслуживание: новые проблемы не открываются
	st.SetMaintenance(id, t0.Add(2*time.Hour).Unix(), "admin: замена диска")
	push(t, st, e, id, t0.Add(11*time.Minute), 10, 99)
	if k := openKeys(st, id); len(k) != 0 {
		t.Fatalf("во время обслуживания открыли %v", k)
	}
}

func TestAvailability(t *testing.T) {
	loc := time.Local
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, loc)
	created := now.AddDate(0, 0, -40).Unix()
	c := func(t time.Time) *int64 { u := t.Unix(); return &u }
	d5 := time.Date(2026, 11, 5, 10, 0, 0, 0, loc)
	probs := []*Problem{
		{Kind: "down", Title: "Нет данных", OpenedAt: d5.Unix(), ClosedAt: c(d5.Add(48 * time.Minute))},
		{Kind: "down", Title: "Узел не в сети", OpenedAt: d5.Add(30 * time.Minute).Unix(), ClosedAt: c(d5.Add(60 * time.Minute))}, // пересекается
		{Kind: "degraded", Title: "CPU", OpenedAt: d5.AddDate(0, 0, 2).Unix(), ClosedAt: c(d5.AddDate(0, 0, 2).Add(time.Hour))},
		{Kind: "down", Title: "Нет данных", OpenedAt: now.Add(-25 * time.Minute).Unix()}, // продолжается
	}
	av := availabilityFor(created, probs, now)
	if len(av.Days) != 30 || av.Days[29].Date != "2026-11-11" {
		t.Fatalf("дни: %+v", av.Days[29])
	}
	byDate := map[string]Day{}
	for _, d := range av.Days {
		byDate[d.Date] = d
	}
	if d := byDate["2026-11-05"]; d.State != "down" || d.DownMin != 60 {
		t.Fatalf("5 ноября: %+v (интервалы должны склеиться в 60 минут)", d)
	}
	if d := byDate["2026-11-07"]; d.State != "warn" {
		t.Fatalf("7 ноября: %+v", d)
	}
	if d := byDate["2026-11-11"]; d.State != "down" || d.DownMin != 25 {
		t.Fatalf("сегодня: %+v", d)
	}
	if av.Pct == nil || *av.Pct >= 100 || *av.Pct < 99.7 {
		t.Fatalf("процент: %v", av.Pct)
	}
	// новое устройство: дни до подключения — none
	av2 := availabilityFor(now.Add(-2*time.Hour).Unix(), nil, now)
	if av2.Days[0].State != "none" || av2.Days[29].State != "ok" || *av2.Pct != 100 {
		t.Fatalf("новое устройство: %+v %v", av2.Days[29], *av2.Pct)
	}
}
