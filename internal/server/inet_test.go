package server

import (
	"context"
	"encoding/csv"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sentra/internal/netprobe"
)

// inetStep записывает одну проверку канала и прогоняет правила.
func inetStep(t *testing.T, st *Store, e *Engine, at time.Time, last map[string]any) {
	t.Helper()
	if err := st.EnsureInetHost(); err != nil {
		t.Fatal(err)
	}
	if err := st.TouchHost(inetHostID, at.Unix(), last, nil); err != nil {
		t.Fatal(err)
	}
	h, _ := st.Host(inetHostID)
	e.Eval(h, at.Add(8*time.Second))
}

func up() map[string]any {
	return map[string]any{"inet_up": 1.0, "router_up": 1.0, "loss5": 0.0, "rtt5": 12.0}
}
func down(router float64) map[string]any {
	return map[string]any{"inet_up": 0.0, "router_up": router}
}

func TestInetOutageNeedsTwoChecksAndIsBackdated(t *testing.T) {
	st := testStore(t)
	e := NewEngine(st)
	t0 := time.Date(2026, 11, 11, 10, 0, 0, 0, time.Local)
	inetStep(t, st, e, t0, up())

	// одна неудачная проверка — ещё не простой, даже если её перечитали дважды
	t1 := t0.Add(30 * time.Second)
	inetStep(t, st, e, t1, down(1))
	h, _ := st.Host(inetHostID)
	e.Eval(h, t1.Add(29*time.Second))
	if k := openKeys(st, inetHostID); len(k) != 0 {
		t.Fatalf("рано открыли простой: %v", k)
	}

	// вторая подряд — простой, и начался он с первой
	inetStep(t, st, e, t0.Add(60*time.Second), down(1))
	ps, _ := st.OpenProblemsForHost(inetHostID)
	if len(ps) != 1 || ps[0].Key != "inet_down" || ps[0].Kind != "down" || ps[0].Title != titleInetProvider {
		t.Fatalf("ждали простой по вине провайдера, есть %+v", ps)
	}
	if ps[0].OpenedAt != t1.Unix() {
		t.Fatalf("начало простоя %d, ждали время первой неудачной проверки %d", ps[0].OpenedAt, t1.Unix())
	}

	// интернет вернулся: закрываем сразу
	inetStep(t, st, e, t0.Add(5*time.Minute), up())
	if k := openKeys(st, inetHostID); len(k) != 0 {
		t.Fatalf("простой не закрылся: %v", k)
	}
}

func TestInetRouterDownIsNotProviderFault(t *testing.T) {
	st := testStore(t)
	e := NewEngine(st)
	t0 := time.Date(2026, 11, 11, 10, 0, 0, 0, time.Local)
	inetStep(t, st, e, t0, down(0))
	inetStep(t, st, e, t0.Add(30*time.Second), down(0))
	if k := openKeys(st, inetHostID); k["router_down"] != 2 || len(k) != 1 {
		t.Fatalf("ждали «нет связи с роутером», есть %v", k)
	}
	// роутер ожил, а интернета всё нет: причина меняется
	inetStep(t, st, e, t0.Add(60*time.Second), down(1))
	if k := openKeys(st, inetHostID); k["inet_down"] != 2 || len(k) != 1 {
		t.Fatalf("ждали смену причины на провайдера, есть %v", k)
	}
}

func TestInetLossWarnsAfterHold(t *testing.T) {
	st := testStore(t)
	e := NewEngine(st)
	t0 := time.Date(2026, 11, 11, 10, 0, 0, 0, time.Local)
	lossy := map[string]any{"inet_up": 1.0, "loss5": 8.0, "rtt5": 20.0}
	for i := 0; i < 4; i++ {
		inetStep(t, st, e, t0.Add(time.Duration(i)*30*time.Second), lossy)
	}
	if k := openKeys(st, inetHostID); len(k) != 0 {
		t.Fatalf("потери ещё не держатся 2 минуты: %v", k)
	}
	inetStep(t, st, e, t0.Add(150*time.Second), lossy)
	if k := openKeys(st, inetHostID); k["inet_loss"] != 1 {
		t.Fatalf("ждали предупреждение о потерях, есть %v", k)
	}
}

func TestInetStaleAndDisabled(t *testing.T) {
	st := testStore(t)
	e := NewEngine(st)
	t0 := time.Date(2026, 11, 11, 10, 0, 0, 0, time.Local)
	inetStep(t, st, e, t0, down(1))
	inetStep(t, st, e, t0.Add(30*time.Second), down(1))
	// коробку выключили на час: простой не закрываем и «нет данных от агента» не заводим
	h, _ := st.Host(inetHostID)
	e.Eval(h, t0.Add(time.Hour))
	if k := openKeys(st, inetHostID); k["inet_down"] != 2 || len(k) != 1 {
		t.Fatalf("после простоя самой коробки ждали тот же простой, есть %v", k)
	}
	// проверку выключили: всё закрывается
	st.SetHostMeta(inetHostID, map[string]any{"disabled": true})
	h, _ = st.Host(inetHostID)
	e.Eval(h, t0.Add(time.Hour))
	if k := openKeys(st, inetHostID); len(k) != 0 {
		t.Fatalf("после выключения проверки остались проблемы: %v", k)
	}
}

func TestLowerMedianIgnoresOneBadTarget(t *testing.T) {
	if v := lowerMedian([]float64{100, 0, 0}); v != 0 {
		t.Fatalf("один заблокированный адрес из трёх не должен давать потери, получили %v", v)
	}
	if v := lowerMedian([]float64{100, 0}); v != 0 {
		t.Fatalf("из двух берём лучший, получили %v", v)
	}
	if v := lowerMedian([]float64{40, 20, 100}); v != 40 {
		t.Fatalf("получили %v", v)
	}
}

// Полный цикл с подменённой проверкой: роутер жив, внешние адреса молчат.
func TestInetCycleAndReport(t *testing.T) {
	st := testStore(t)
	s := &Server{st: st, vm: NewVM("http://127.0.0.1:1"), eng: NewEngine(st), inet: newInetPoller()}
	st.SetSetting("inet", `{"enabled":true,"router":"192.168.1.1","targets":[{"name":"Яндекс","addr":"77.88.8.8"},{"name":"Google","addr":"8.8.8.8"}]}`)
	internet := false
	s.inet.probe = func(_ context.Context, addr string, _ []int) netprobe.Result {
		if addr == "192.168.1.1" || internet {
			return netprobe.Result{Sent: 5, Recv: 5, AvgMs: 3, Via: "icmp"}
		}
		return netprobe.Result{Sent: 5, Via: "icmp"}
	}
	ctx := context.Background()
	s.inetCycle(ctx)
	// вторая проверка должна иметь другое время: сдвигаем первую в прошлое
	st.db.Exec(`UPDATE hosts SET last_seen=last_seen-30, created_at=created_at-60 WHERE id=?`, inetHostID)
	s.eng.inet.seen -= 30
	s.eng.inet.first -= 30
	s.inetCycle(ctx)
	if k := openKeys(st, inetHostID); k["inet_down"] != 2 {
		t.Fatalf("ждали простой интернета, есть %v", k)
	}
	h, _ := st.Host(inetHostID)
	if v := view(h, nil); v.Status != "ok" && v.Status != "critical" {
		t.Fatalf("неожиданный статус %q", v.Status)
	}

	rep, err := s.outages(30, time.Now())
	if err != nil || len(rep.Outages) != 1 || rep.Outages[0].Cause != "provider" {
		t.Fatalf("отчёт: %+v, %v", rep, err)
	}
	if rep.ProviderSec < 29 || rep.DownSeconds != rep.ProviderSec || rep.LocalSec != 0 {
		t.Fatalf("длительность: всего %d, провайдер %d, локально %d", rep.DownSeconds, rep.ProviderSec, rep.LocalSec)
	}

	internet = true
	s.inetCycle(ctx)
	if k := openKeys(st, inetHostID); len(k) != 0 {
		t.Fatalf("простой не закрылся: %v", k)
	}

	rec := httptest.NewRecorder()
	s.inetReport(rec, httptest.NewRequest("GET", "/api/inet/report.csv?days=30", nil), &User{Login: "admin"})
	body := rec.Body.String()
	if !strings.HasPrefix(body, "\xEF\xBB\xBF") {
		t.Fatal("нет BOM: Excel покажет кракозябры")
	}
	r := csv.NewReader(strings.NewReader(strings.TrimPrefix(body, "\xEF\xBB\xBF")))
	r.Comma, r.FieldsPerRecord = ';', -1
	rows, err := r.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	lastRow := rows[len(rows)-1]
	if len(lastRow) != 6 || lastRow[0] != "1" || lastRow[5] != causeText["provider"] || lastRow[2] == "продолжается" {
		t.Fatalf("строка простоя в CSV: %q", lastRow)
	}
}

func TestInetConfigValidation(t *testing.T) {
	for addr, ok := range map[string]bool{"192.168.1.1": true, "ya.ru": true, "router": true, "::1": false, "a b": false, "http://ya.ru": false, "": false} {
		if validAddr(addr) != ok {
			t.Errorf("validAddr(%q) = %v, ждали %v", addr, !ok, ok)
		}
	}
}
