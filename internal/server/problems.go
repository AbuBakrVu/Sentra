package server

import (
	"database/sql"
	"fmt"
	"log"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"sentra/internal/proto"
)

// ---------- хранилище ----------

const problemsSchema = `
CREATE TABLE IF NOT EXISTS problems (
  id INTEGER PRIMARY KEY,
  host_id TEXT NOT NULL,
  key TEXT NOT NULL,           -- cpu, mem, disk:/, offline, node_offline, stopped
  kind TEXT NOT NULL,          -- down (недоступно), degraded (работает, но плохо), stopped (выключено)
  severity INTEGER NOT NULL,   -- 0 нет, 1 внимание, 2 авария
  max_severity INTEGER NOT NULL,
  title TEXT NOT NULL,
  opened_at INTEGER NOT NULL,
  closed_at INTEGER,
  ack_by TEXT NOT NULL DEFAULT '',
  ack_at INTEGER NOT NULL DEFAULT 0,
  ack_comment TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS problems_open ON problems(host_id, closed_at);
CREATE INDEX IF NOT EXISTS problems_time ON problems(opened_at);
`

type Problem struct {
	ID          int64  `json:"id"`
	HostID      string `json:"host_id"`
	HostName    string `json:"host_name,omitempty"`
	Key         string `json:"key"`
	Kind        string `json:"kind"`
	Severity    int    `json:"severity"`
	MaxSeverity int    `json:"max_severity"`
	Title       string `json:"title"`
	OpenedAt    int64  `json:"opened_at"`
	ClosedAt    *int64 `json:"closed_at"`
	AckBy       string `json:"ack_by,omitempty"`
	AckAt       int64  `json:"ack_at,omitempty"`
	AckComment  string `json:"ack_comment,omitempty"`
}

const problemCols = `id, host_id, key, kind, severity, max_severity, title, opened_at, closed_at, ack_by, ack_at, ack_comment`

func scanProblem(sc interface{ Scan(...any) error }) (*Problem, error) {
	var p Problem
	var closed sql.NullInt64
	if err := sc.Scan(&p.ID, &p.HostID, &p.Key, &p.Kind, &p.Severity, &p.MaxSeverity, &p.Title, &p.OpenedAt, &closed, &p.AckBy, &p.AckAt, &p.AckComment); err != nil {
		return nil, err
	}
	if closed.Valid {
		c := closed.Int64
		p.ClosedAt = &c
	}
	return &p, nil
}

func (s *Store) queryProblems(q string, args ...any) ([]*Problem, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Problem{}
	for rows.Next() {
		p, err := scanProblem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) OpenProblems() ([]*Problem, error) {
	return s.queryProblems(`SELECT ` + problemCols + ` FROM problems WHERE closed_at IS NULL ORDER BY opened_at`)
}

func (s *Store) OpenProblemsForHost(hostID string) ([]*Problem, error) {
	return s.queryProblems(`SELECT `+problemCols+` FROM problems WHERE host_id=? AND closed_at IS NULL`, hostID)
}

// ProblemsSince — всё, что было открыто хотя бы мгновение после t.
func (s *Store) ProblemsSince(t int64) ([]*Problem, error) {
	return s.queryProblems(`SELECT `+problemCols+` FROM problems WHERE closed_at IS NULL OR closed_at >= ? ORDER BY opened_at`, t)
}

func (s *Store) Problem(id int64) (*Problem, error) {
	return scanProblem(s.db.QueryRow(`SELECT `+problemCols+` FROM problems WHERE id=?`, id))
}

func (s *Store) OpenProblem(hostID, key, kind string, sev int, title string, at int64) error {
	_, err := s.db.Exec(`INSERT INTO problems(host_id, key, kind, severity, max_severity, title, opened_at) VALUES(?,?,?,?,?,?,?)`,
		hostID, key, kind, sev, sev, title, at)
	return err
}

func (s *Store) UpdateProblem(id int64, sev int, title string) error {
	_, err := s.db.Exec(`UPDATE problems SET severity=?, max_severity=MAX(max_severity, ?), title=? WHERE id=?`, sev, sev, title, id)
	return err
}

func (s *Store) CloseProblem(id int64, at int64) error {
	_, err := s.db.Exec(`UPDATE problems SET closed_at=? WHERE id=? AND closed_at IS NULL`, at, id)
	return err
}

func (s *Store) AckProblem(id int64, by, comment string) error {
	r, err := s.db.Exec(`UPDATE problems SET ack_by=?, ack_at=?, ack_comment=? WHERE id=? AND closed_at IS NULL`, by, time.Now().Unix(), comment, id)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) SetMaintenance(hostID string, until int64, comment string) error {
	_, err := s.db.Exec(`UPDATE hosts SET maint_until=?, maint_comment=? WHERE id=?`, until, comment, hostID)
	return err
}

// CleanupProblems удаляет закрытые проблемы старше срока хранения.
func (s *Store) CleanupProblems(before int64) {
	s.db.Exec(`DELETE FROM problems WHERE closed_at IS NOT NULL AND closed_at < ?`, before)
	s.db.Exec(`DELETE FROM problems WHERE host_id NOT IN (SELECT id FROM hosts)`)
}

// ---------- правила ----------

// Стартовые пороги из ТЗ. Позже их можно будет менять для каждого устройства.
type rule struct {
	key          string
	warn, crit   float64
	holdW, holdC time.Duration
	hyst         float64 // закрыть, только когда значение ниже warn на столько
	title        func(v float64) string
}

var (
	ruleCPU = rule{"cpu", 85, 95, 10 * time.Minute, 10 * time.Minute, 5,
		func(v float64) string { return fmt.Sprintf("Загрузка CPU %.0f%%", v) }}
	ruleMem = rule{"mem", 85, 95, 10 * time.Minute, 5 * time.Minute, 3,
		func(v float64) string { return fmt.Sprintf("Память занята на %.0f%%", v) }}
)

const (
	diskWarn, diskCrit = 85.0, 95.0
	diskMinFree        = 2 << 30 // авария, если свободно меньше 2 ГБ на разделе от 20 ГБ
	retention          = 90 * 24 * time.Hour
)

type desired struct {
	kind  string
	sev   int
	title string
	since int64 // когда началось на самом деле; 0 — сейчас
}

type pending struct{ w, c time.Time }

// Engine превращает метрики и состояние устройств в проблемы с началом и концом.
type Engine struct {
	mu   sync.Mutex
	st   *Store
	pend map[string]*pending
	inet inetFails
}

func NewEngine(st *Store) *Engine {
	return &Engine{st: st, pend: map[string]*pending{}}
}

// level возвращает уровень с учётом задержки: всплеск на 30 секунд — не проблема.
func (e *Engine) level(hostID string, r rule, v float64, now time.Time, critExtra bool, open *Problem) int {
	k := hostID + "|" + r.key
	p := e.pend[k]
	if p == nil {
		p = &pending{}
		e.pend[k] = p
	}
	condW, condC := v > r.warn, v > r.crit || critExtra
	if condW || condC {
		if p.w.IsZero() {
			p.w = now
		}
	} else {
		p.w = time.Time{}
	}
	if condC {
		if p.c.IsZero() {
			p.c = now
		}
	} else {
		p.c = time.Time{}
	}
	switch {
	case condC && now.Sub(p.c) >= r.holdC:
		return 2
	case (condW || condC) && now.Sub(p.w) >= r.holdW:
		return 1
	case open != nil && v > r.warn-r.hyst:
		// гистерезис: не закрываем проблему, пока значение у самого порога
		if open.Severity == 2 && condW {
			return 2
		}
		return 1
	}
	return 0
}

// Eval сверяет состояние устройства с открытыми проблемами: открывает новые,
// обновляет текущие и закрывает те, что прошли.
func (e *Engine) Eval(h *Host, now time.Time) {
	if h.LastSeen == 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	openList, err := e.st.OpenProblemsForHost(h.ID)
	if err != nil {
		log.Printf("проблемы %s: %v", h.ID, err)
		return
	}
	open := map[string]*Problem{}
	for _, p := range openList {
		open[p.Key] = p
	}
	want := map[string]desired{}
	keep := false // не трогать прочие открытые проблемы (данных нет)

	pve := strings.HasPrefix(h.Kind, "pve-")
	status, _ := h.Meta["status"].(string)
	gone, _ := h.Meta["gone"].(float64)
	switch {
	case h.Kind == "inet":
		keep = e.evalInet(h, now, open, want)
	case pve && gone == 1:
		// машины больше нет в Proxmox: всё закрываем
	case now.Sub(time.Unix(h.LastSeen, 0)) > offlineAfter:
		t := "Нет данных от агента"
		if pve {
			t = "Нет данных от Proxmox"
			if n := e.machinesOn(h); h.Kind == "pve-node" && n > 0 {
				t = fmt.Sprintf("Нет данных от Proxmox об узле и %d %s на нём", n, pluralRu(n, "машине", "машинах", "машинах"))
			}
		}
		want["offline"] = desired{"down", 2, t, 0}
		keep = true
	case h.Kind == "pve-node" && status != "online":
		t := "Узел не в сети кластера"
		if n := e.machinesOn(h); n > 0 {
			t = fmt.Sprintf("Узел не в сети кластера, с ним недоступны %d %s", n, pluralRu(n, "машина", "машины", "машин"))
		}
		want["node_offline"] = desired{"down", 2, t, 0}
	case pve && h.Meta["node_offline"] == true:
		want["node_offline"] = desired{"down", 2, "Узел Proxmox не в сети", 0}
	case pve && h.Kind != "pve-node" && status != "running":
		want["stopped"] = desired{"stopped", 0, "Выключена", 0}
	default:
		m := h.LastMetrics
		if v, ok := m[proto.MCPUUsage].(float64); ok {
			if l := e.level(h.ID, ruleCPU, v, now, false, open["cpu"]); l > 0 {
				want["cpu"] = desired{"degraded", l, ruleCPU.title(v), 0}
			}
		}
		if v, ok := m[proto.MMemUsedPct].(float64); ok {
			if l := e.level(h.ID, ruleMem, v, now, false, open["mem"]); l > 0 {
				want["mem"] = desired{"degraded", l, ruleMem.title(v), 0}
			}
		}
		if fs, ok := m["fs"].(map[string]any); ok {
			for mount, raw := range fs {
				arr, _ := raw.([]any)
				if len(arr) < 3 {
					continue
				}
				pct, _ := arr[0].(float64)
				free, _ := arr[1].(float64)
				total, _ := arr[2].(float64)
				low := total >= 20<<30 && free < diskMinFree
				r := rule{"disk:" + mount, diskWarn, diskCrit, 0, 0, 2, nil}
				if l := e.level(h.ID, r, pct, now, low, open[r.key]); l > 0 {
					t := fmt.Sprintf("Диск %s заполнен на %.0f%%", mount, pct)
					if low {
						t = fmt.Sprintf("Диск %s: свободно %s", mount, humanBytes(free))
					}
					want[r.key] = desired{"degraded", l, t, 0}
				}
			}
		}
	}

	maint := h.MaintUntil > now.Unix()
	at := now.Unix()
	for key, d := range want {
		if p := open[key]; p != nil {
			if p.Severity != d.sev || p.Title != d.title {
				e.st.UpdateProblem(p.ID, d.sev, d.title)
			}
			continue
		}
		if maint && d.kind != "stopped" {
			continue // во время обслуживания новых проблем не заводим
		}
		from := at
		if d.since != 0 && d.since < at {
			from = d.since
		}
		if err := e.st.OpenProblem(h.ID, key, d.kind, d.sev, d.title, from); err != nil {
			log.Printf("открытие проблемы %s %s: %v", h.ID, key, err)
		}
	}
	for key, p := range open {
		if _, ok := want[key]; ok {
			continue
		}
		if keep && key != "offline" {
			continue
		}
		e.st.CloseProblem(p.ID, at)
	}
}

// Sweep раз в 30 секунд проверяет все устройства: так замечаем, что агент
// замолчал, и снимаем истёкшее обслуживание.
func (e *Engine) Sweep(now time.Time) {
	hs, err := e.st.Hosts()
	if err != nil {
		return
	}
	for _, h := range hs {
		if h.MaintUntil != 0 && h.MaintUntil <= now.Unix() {
			e.st.SetMaintenance(h.ID, 0, "")
			h.MaintUntil = 0
		}
		e.Eval(h, now)
	}
}

func humanBytes(b float64) string {
	u := []string{"Б", "КБ", "МБ", "ГБ", "ТБ"}
	i := 0
	for b >= 1024 && i < len(u)-1 {
		b /= 1024
		i++
	}
	return strings.Replace(strconv.FormatFloat(b, 'f', 1, 64), ".", ",", 1) + " " + u[i]
}

// ---------- доступность ----------

type Day struct {
	Date    string   `json:"date"`  // 2026-11-11
	State   string   `json:"state"` // ok, warn, down, off, none
	DownMin int      `json:"down_min"`
	Notes   []string `json:"notes"`
}

type Availability struct {
	Pct   *float64 `json:"pct"`   // nil, если данных нет совсем
	Young bool     `json:"young"` // наблюдаем меньше суток: процент ещё ни о чём не говорит
	Days  []Day    `json:"days"`
}

const youngFor = 24 * time.Hour

const availDays = 30

type span struct{ a, b int64 }

func merge(sp []span) []span {
	if len(sp) < 2 {
		return sp
	}
	sort.Slice(sp, func(i, j int) bool { return sp[i].a < sp[j].a })
	out := []span{sp[0]}
	for _, s := range sp[1:] {
		last := &out[len(out)-1]
		if s.a <= last.b {
			if s.b > last.b {
				last.b = s.b
			}
		} else {
			out = append(out, s)
		}
	}
	return out
}

func overlap(sp []span, a, b int64) int64 {
	var t int64
	for _, s := range sp {
		lo, hi := max(s.a, a), min(s.b, b)
		if hi > lo {
			t += hi - lo
		}
	}
	return t
}

// availabilityFor считает доступность по дням: «недоступно» — только проблемы
// вида down (нет связи, узел не в сети). Высокий CPU — это «внимание», не простой.
func availabilityFor(created int64, probs []*Problem, now time.Time) Availability {
	loc := now.Location()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	nowU := now.Unix()
	var down, off []span
	for _, p := range probs {
		end := nowU
		if p.ClosedAt != nil {
			end = *p.ClosedAt
		}
		switch p.Kind {
		case "down":
			down = append(down, span{p.OpenedAt, end})
		case "stopped":
			off = append(off, span{p.OpenedAt, end})
		}
	}
	down, off = merge(down), merge(off)

	av := Availability{Days: make([]Day, availDays), Young: nowU-created < int64(youngFor.Seconds())}
	var total, downSum int64
	for i := 0; i < availDays; i++ {
		start := today.AddDate(0, 0, i-(availDays-1))
		a, b := start.Unix(), start.AddDate(0, 0, 1).Unix()
		if b > nowU {
			b = nowU
		}
		d := Day{Date: start.Format("2006-01-02"), State: "ok", Notes: []string{}}
		if b <= created {
			d.State = "none"
			av.Days[i] = d
			continue
		}
		if a < created {
			a = created
		}
		win := b - a
		dn, of := overlap(down, a, b), overlap(off, a, b)
		d.DownMin = int(math.Ceil(float64(dn) / 60))
		var warn bool
		for _, p := range probs {
			end := nowU
			if p.ClosedAt != nil {
				end = *p.ClosedAt
			}
			if p.OpenedAt >= b || end <= a || p.Kind == "stopped" {
				continue
			}
			if p.Kind == "degraded" {
				warn = true
			}
			if len(d.Notes) < 3 {
				d.Notes = append(d.Notes, noteFor(p, end, nowU, loc))
			}
		}
		switch {
		case dn > 0:
			d.State = "down"
		case warn:
			d.State = "warn"
		case of*2 >= win:
			d.State = "off"
		}
		total += win - of
		downSum += dn
		av.Days[i] = d
	}
	if total > 0 {
		pct := math.Floor((1-float64(downSum)/float64(total))*10000) / 100
		av.Pct = &pct
	}
	return av
}

func noteFor(p *Problem, end, now int64, loc *time.Location) string {
	t := time.Unix(p.OpenedAt, 0).In(loc).Format("15:04")
	dur := "продолжается"
	if p.ClosedAt != nil {
		dur = humanDur(end - p.OpenedAt)
	} else {
		dur = humanDur(now-p.OpenedAt) + ", продолжается"
	}
	return t + " " + p.Title + " (" + dur + ")"
}

func humanDur(sec int64) string {
	if sec < 60 {
		return "меньше минуты"
	}
	m := sec / 60
	if m < 60 {
		return fmt.Sprintf("%d мин", m)
	}
	h := m / 60
	if h < 24 {
		if m%60 == 0 {
			return fmt.Sprintf("%d ч", h)
		}
		return fmt.Sprintf("%d ч %d мин", h, m%60)
	}
	return fmt.Sprintf("%d дн %d ч", h/24, h%24)
}

// ---------- API ----------

// Зависимые проблемы: машина недоступна, потому что недоступен её узел или
// всё подключение к Proxmox. Причина одна, и она уже есть у узла, поэтому в
// списках и счётчиках их не показываем — иначе один упавший узел даёт десятки
// одинаковых проблем. В базе они остаются: по ним считается доступность
// машины и видна её история на её странице.
func dependent(p *Problem, kind string) bool {
	return (kind == "pve-qemu" || kind == "pve-lxc") && p.Kind == "down" && (p.Key == "node_offline" || p.Key == "offline")
}

// machinesOn — сколько машин стоит на узле: недоступны вместе с ним.
func (e *Engine) machinesOn(h *Host) int {
	if h.Kind != "pve-node" {
		return 0
	}
	node, _ := h.Meta["node"].(string)
	return e.st.PVEMachinesOn(h.SourceID, node)
}

func pluralRu(n int, one, few, many string) string {
	n10, n100 := n%10, n%100
	switch {
	case n10 == 1 && n100 != 11:
		return one
	case n10 >= 2 && n10 <= 4 && (n100 < 12 || n100 > 14):
		return few
	}
	return many
}

func (s *Server) listProblems(w http.ResponseWriter, r *http.Request, _ *User) {
	q := r.URL.Query()
	days, _ := strconv.Atoi(q.Get("days"))
	if days <= 0 || days > 90 {
		days = 30
	}
	var list []*Problem
	var err error
	switch q.Get("state") {
	case "open", "":
		list, err = s.st.OpenProblems()
	default:
		list, err = s.st.ProblemsSince(time.Now().AddDate(0, 0, -days).Unix())
	}
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	names := map[string]string{}
	kinds := map[string]string{}
	if hs, err := s.st.Hosts(); err == nil {
		for _, h := range hs {
			names[h.ID] = h.Name
			kinds[h.ID] = h.Kind
		}
	}
	host := q.Get("host")
	out := []*Problem{}
	for i := len(list) - 1; i >= 0; i-- { // новые сверху
		p := list[i]
		if p.Kind == "stopped" || (host != "" && p.HostID != host) {
			continue
		}
		if host == "" && dependent(p, kinds[p.HostID]) {
			continue // на странице самой машины такие проблемы видны
		}
		if q.Get("state") == "closed" && p.ClosedAt == nil {
			continue
		}
		p.HostName = names[p.HostID]
		out = append(out, p)
	}
	writeJSON(w, 200, out)
}

func (s *Server) ackProblem(w http.ResponseWriter, r *http.Request, u *User) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var in struct{ Comment string }
	if err := readJSON(r, &in, 4096); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	c := strings.TrimSpace(in.Comment)
	if len([]rune(c)) > 500 {
		httpErr(w, 400, "комментарий до 500 символов")
		return
	}
	if err := s.st.AckProblem(id, u.Login, c); err != nil {
		httpErr(w, 404, "проблема не найдена или уже закрыта")
		return
	}
	p, _ := s.st.Problem(id)
	writeJSON(w, 200, p)
}

func (s *Server) setMaintenance(w http.ResponseWriter, r *http.Request, u *User) {
	id := r.PathValue("id")
	h, err := s.st.Host(id)
	if err != nil {
		httpErr(w, 404, "хост не найден")
		return
	}
	var in struct {
		Minutes int
		Comment string
	}
	if err := readJSON(r, &in, 4096); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	if in.Minutes < 0 || in.Minutes > 7*24*60 {
		httpErr(w, 400, "обслуживание от 0 минут до 7 дней")
		return
	}
	until := int64(0)
	comment := ""
	if in.Minutes > 0 {
		until = time.Now().Add(time.Duration(in.Minutes) * time.Minute).Unix()
		comment = strings.TrimSpace(in.Comment)
		if comment == "" {
			comment = "без комментария"
		}
		comment = u.Login + ": " + comment
		log.Printf("обслуживание %s (%s) до %s, %s", h.Name, h.ID, time.Unix(until, 0).Format("02.01 15:04"), comment)
	}
	if err := s.st.SetMaintenance(id, until, comment); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"maint_until": until, "maint_comment": comment})
}

// StartEngine запускает периодическую проверку и очистку журнала.
func (s *Server) StartEngine(stop <-chan struct{}) {
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		clean := time.Now()
		s.eng.Sweep(time.Now())
		for {
			select {
			case <-stop:
				return
			case now := <-t.C:
				s.eng.Sweep(now)
				if now.Sub(clean) > 6*time.Hour {
					s.st.CleanupProblems(now.Add(-retention).Unix())
					clean = now
				}
			}
		}
	}()
}
