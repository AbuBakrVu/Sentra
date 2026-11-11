package server

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"sentra/internal/netprobe"
	"sentra/internal/proto"
)

// Интернет-канал: коробка раз в 30 секунд проверяет роутер и несколько внешних
// адресов. Так видно, пропал интернет у провайдера или связь внутри офиса.

const (
	inetHostID = "internet"
	inetEvery  = 30 * time.Second
	inetWindow = 10 // сколько последних проверок усредняем: 5 минут

	mInetUp   = "inet_up"
	mInetLoss = "inet_loss_percent"
	mInetRTT  = "inet_rtt_ms"

	// устройство в журнале уже называется «Интернет», поэтому в тексте его не повторяем
	titleInetProvider = "Нет связи с внешними адресами, роутер отвечает"
	titleInetUnknown  = "Нет связи с внешними адресами"
	titleRouterDown   = "Нет связи с роутером"
	titleInetProvOld  = "Интернет недоступен: роутер отвечает, внешние адреса нет" // записи первой версии
)

var (
	ruleInetLoss = rule{"inet_loss", 3, 15, 2 * time.Minute, 2 * time.Minute, 1,
		func(v float64) string { return fmt.Sprintf("Потери пакетов %.0f%% за 5 минут", v) }}
	ruleInetRTT = rule{"inet_rtt", 150, 400, 5 * time.Minute, 5 * time.Minute, 20,
		func(v float64) string { return fmt.Sprintf("Высокая задержка: %.0f мс", v) }}
)

type InetTarget struct {
	Name string `json:"name"`
	Addr string `json:"addr"`
}

type InetConfig struct {
	Enabled bool         `json:"enabled"`
	Router  string       `json:"router"` // адрес роутера в офисе, можно не указывать
	Targets []InetTarget `json:"targets"`
}

func defaultInetConfig() InetConfig {
	return InetConfig{Enabled: true, Targets: []InetTarget{
		{"Яндекс", "77.88.8.8"},
		{"Google", "8.8.8.8"},
		{"Cloudflare", "1.1.1.1"},
	}}
}

func (s *Server) inetConfig() InetConfig {
	c := defaultInetConfig()
	if raw := s.st.Setting("inet"); raw != "" {
		var saved InetConfig
		if json.Unmarshal([]byte(raw), &saved) == nil && len(saved.Targets) > 0 {
			c = saved
		}
	}
	return c
}

// ---------- хранилище ----------

func (s *Store) EnsureInetHost() error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO hosts(id, name, key_hash, kind, created_at) VALUES(?,?,?,?,?)`,
		inetHostID, "Интернет", "-", "inet", time.Now().Unix())
	return err
}

func (s *Store) SetHostMeta(id string, meta map[string]any) error {
	mj, _ := json.Marshal(meta)
	_, err := s.db.Exec(`UPDATE hosts SET meta=? WHERE id=?`, string(mj), id)
	return err
}

// ---------- опрос ----------

type inetPoller struct {
	prober *netprobe.Prober
	probe  func(ctx context.Context, addr string, ports []int) netprobe.Result
	kick   chan struct{}
	mu     sync.Mutex
	loss   []float64 // последние проверки, когда интернет был
	rtt    []float64
}

func newInetPoller() *inetPoller {
	p := &inetPoller{prober: netprobe.New(), kick: make(chan struct{}, 1)}
	p.probe = p.prober.Probe
	return p
}

func (p *inetPoller) reset() {
	p.mu.Lock()
	p.loss, p.rtt = nil, nil
	p.mu.Unlock()
}

// push добавляет проверку в окно и возвращает средние за 5 минут.
func (p *inetPoller) push(loss, rtt float64) (float64, float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	add := func(a []float64, v float64) []float64 {
		a = append(a, v)
		if len(a) > inetWindow {
			a = a[len(a)-inetWindow:]
		}
		return a
	}
	p.loss, p.rtt = add(p.loss, loss), add(p.rtt, rtt)
	return mean(p.loss), mean(p.rtt)
}

func mean(a []float64) float64 {
	if len(a) == 0 {
		return 0
	}
	var s float64
	for _, v := range a {
		s += v
	}
	return s / float64(len(a))
}

// lowerMedian устойчива к одному «плохому» адресу: если из трёх внешних
// адресов один заблокирован, канал всё равно считается исправным.
func lowerMedian(a []float64) float64 {
	if len(a) == 0 {
		return 0
	}
	b := append([]float64(nil), a...)
	sort.Float64s(b)
	return b[(len(b)-1)/2]
}

// StartInet запускает фоновую проверку интернет-канала. Останавливается с ctx.
func (s *Server) StartInet(ctx context.Context) {
	if s.inetConfig().Enabled {
		if err := s.st.EnsureInetHost(); err != nil {
			log.Printf("интернет: %v", err)
		}
	}
	go func() {
		t := time.NewTicker(inetEvery)
		defer t.Stop()
		s.inetCycle(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.inetCycle(ctx)
			case <-s.inet.kick:
				s.inetCycle(ctx)
			}
		}
	}()
}

type inetTargetState struct {
	Name string   `json:"name"`
	Addr string   `json:"addr"`
	Role string   `json:"role"` // router или ext
	Up   bool     `json:"up"`
	Loss float64  `json:"loss"`
	RTT  *float64 `json:"rtt"` // мс; null, если ответа нет
	Via  string   `json:"via"`
}

func (s *Server) inetCycle(ctx context.Context) {
	cfg := s.inetConfig()
	if !cfg.Enabled {
		return
	}
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	var ts []inetTargetState
	if cfg.Router != "" {
		ts = append(ts, inetTargetState{Name: "Роутер", Addr: cfg.Router, Role: "router"})
	}
	for _, t := range cfg.Targets {
		ts = append(ts, inetTargetState{Name: t.Name, Addr: t.Addr, Role: "ext"})
	}
	var wg sync.WaitGroup
	for i := range ts {
		wg.Add(1)
		go func(t *inetTargetState) {
			defer wg.Done()
			ports := []int{443, 53}
			if t.Role == "router" {
				ports = []int{80, 443}
			}
			r := s.inet.probe(ctx, t.Addr, ports)
			t.Up, t.Loss, t.Via = r.Up(), round2(r.Loss()), r.Via
			if r.Up() {
				v := round2(r.AvgMs)
				t.RTT = &v
			}
		}(&ts[i])
	}
	wg.Wait()
	if ctx.Err() != nil && started.Add(24*time.Second).After(time.Now()) {
		return // сервер останавливается: неполную проверку не записываем
	}

	last := map[string]any{"targets": ts, "method": s.inet.prober.Method()}
	samples := []proto.Sample{}
	var losses, rtts []float64
	up := false
	for _, t := range ts {
		l := map[string]string{"target": t.Name}
		samples = append(samples, proto.Sample{Name: mInetLoss, Labels: l, Value: t.Loss})
		if t.RTT != nil {
			samples = append(samples, proto.Sample{Name: mInetRTT, Labels: l, Value: *t.RTT})
		}
		if t.Role == "router" {
			last["router_up"] = b2f(t.Up)
			continue
		}
		losses = append(losses, t.Loss)
		if t.Up {
			up = true
			rtts = append(rtts, *t.RTT)
		}
	}
	last[mInetUp] = b2f(up)
	samples = append(samples, proto.Sample{Name: mInetUp, Value: b2f(up)})
	if up {
		loss, rtt := lowerMedian(losses), lowerMedian(rtts)
		loss5, rtt5 := s.inet.push(loss, rtt)
		last["loss"], last["rtt"] = loss, rtt
		last["loss5"], last["rtt5"] = round2(loss5), round2(rtt5)
	}

	if err := s.st.EnsureInetHost(); err != nil {
		log.Printf("интернет: %v", err)
		return
	}
	ictx, icancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	if err := s.vm.Import(ictx, inetHostID, started.UnixMilli(), samples); err != nil {
		log.Printf("интернет: запись метрик: %v", err) // журнал простоев от хранилища метрик не зависит
	}
	icancel()
	if err := s.st.TouchHost(inetHostID, started.Unix(), last, nil); err != nil {
		log.Printf("интернет: %v", err)
		return
	}
	if h, err := s.st.Host(inetHostID); err == nil {
		s.eng.Eval(h, time.Now())
	}
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// ---------- правила ----------

// inetFails считает подряд идущие неудачные проверки. Одна неудача — ещё не
// простой: проблему заводим со второй, но время начала ставим по первой.
type inetFails struct {
	seen  int64
	n     int
	first int64
}

// evalInet заполняет want и возвращает true, если открытые проблемы трогать нельзя.
func (e *Engine) evalInet(h *Host, now time.Time, open map[string]*Problem, want map[string]desired) bool {
	if h.Meta["disabled"] == true {
		return false // проверку выключили: всё закрываем
	}
	if now.Sub(time.Unix(h.LastSeen, 0)) > offlineAfter {
		return true // коробка сама не работала: о канале ничего не знаем
	}
	m := h.LastMetrics
	up := num(m, mInetUp) == 1
	f := &e.inet
	if h.LastSeen != f.seen {
		f.seen = h.LastSeen
		if up {
			f.n = 0
		} else if f.n++; f.n == 1 {
			f.first = h.LastSeen
		}
	}
	if !up {
		wasDown := open["inet_down"] != nil || open["router_down"] != nil
		if f.n < 2 && !wasDown {
			return true // ждём вторую проверку
		}
		key, title := "inet_down", titleInetUnknown
		if r, ok := m["router_up"].(float64); ok {
			if r == 1 {
				title = titleInetProvider
			} else {
				key, title = "router_down", titleRouterDown
			}
		}
		d := desired{kind: "down", sev: 2, title: title, since: f.first}
		if wasDown && open[key] == nil {
			d.since = 0 // причина сменилась посреди простоя
		}
		want[key] = d
		return false
	}
	if v, ok := m["loss5"].(float64); ok {
		if l := e.level(h.ID, ruleInetLoss, v, now, false, open[ruleInetLoss.key]); l > 0 {
			want[ruleInetLoss.key] = desired{kind: "degraded", sev: l, title: ruleInetLoss.title(v)}
		}
	}
	if v, ok := m["rtt5"].(float64); ok {
		if l := e.level(h.ID, ruleInetRTT, v, now, false, open[ruleInetRTT.key]); l > 0 {
			want[ruleInetRTT.key] = desired{kind: "degraded", sev: l, title: ruleInetRTT.title(v)}
		}
	}
	return false
}

// ---------- простои ----------

type Outage struct {
	ID       int64  `json:"id"`
	Cause    string `json:"cause"` // provider, unknown, local
	Title    string `json:"title"`
	OpenedAt int64  `json:"opened_at"`
	ClosedAt *int64 `json:"closed_at"`
	Seconds  int64  `json:"seconds"`
}

type OutageReport struct {
	Days        int      `json:"days"`
	From        int64    `json:"from"`
	To          int64    `json:"to"`
	Outages     []Outage `json:"outages"` // новые сверху
	DownSeconds int64    `json:"down_seconds"`
	ProviderSec int64    `json:"provider_seconds"` // роутер отвечал, внешние адреса нет
	LocalSec    int64    `json:"local_seconds"`    // не отвечал сам роутер
	Pct         *float64 `json:"pct"`
	Young       bool     `json:"young"` // проверка работает меньше суток
}

var causeText = map[string]string{
	"provider": "Канал провайдера: роутер отвечал, внешние адреса нет",
	"unknown":  "Не определена: адрес роутера не был указан",
	"local":    "Локальная сеть: не отвечал роутер",
}

func (s *Server) outages(days int, now time.Time) (*OutageReport, error) {
	from := now.AddDate(0, 0, -days).Unix()
	rep := &OutageReport{Days: days, From: from, To: now.Unix(), Outages: []Outage{}}
	h, err := s.st.Host(inetHostID)
	if err != nil {
		return rep, nil // проверку ещё ни разу не включали
	}
	if h.CreatedAt > rep.From {
		rep.From = h.CreatedAt
	}
	rep.Young = rep.To-rep.From < int64(youngFor.Seconds())
	all, err := s.st.ProblemsSince(from)
	if err != nil {
		return nil, err
	}
	var down, prov, local []span
	for i := len(all) - 1; i >= 0; i-- {
		p := all[i]
		if p.HostID != inetHostID || p.Kind != "down" {
			continue
		}
		end := now.Unix()
		if p.ClosedAt != nil {
			end = *p.ClosedAt
		}
		o := Outage{ID: p.ID, Title: p.Title, OpenedAt: p.OpenedAt, ClosedAt: p.ClosedAt, Seconds: end - p.OpenedAt, Cause: "unknown"}
		sp := span{p.OpenedAt, end}
		switch {
		case p.Key == "router_down":
			o.Cause = "local"
			local = append(local, sp)
		case p.Title == titleInetProvider || p.Title == titleInetProvOld:
			o.Cause = "provider"
			prov = append(prov, sp)
		}
		down = append(down, sp)
		rep.Outages = append(rep.Outages, o)
	}
	rep.DownSeconds = overlap(merge(down), rep.From, rep.To)
	rep.ProviderSec = overlap(merge(prov), rep.From, rep.To)
	rep.LocalSec = overlap(merge(local), rep.From, rep.To)
	if total := rep.To - rep.From; total > 0 && h.LastSeen != 0 {
		pct := math.Floor((1-float64(rep.DownSeconds)/float64(total))*10000) / 100
		rep.Pct = &pct
	}
	return rep, nil
}

// exactDur — длительность с секундами: в претензии важна точность.
func exactDur(sec int64) string {
	h, m, s := sec/3600, sec%3600/60, sec%60
	switch {
	case h > 0:
		return fmt.Sprintf("%d ч %d мин %d с", h, m, s)
	case m > 0:
		return fmt.Sprintf("%d мин %d с", m, s)
	}
	return fmt.Sprintf("%d с", s)
}

func reportDays(r *http.Request) int {
	d, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if d != 7 && d != 90 {
		d = 30
	}
	return d
}

// ---------- API ----------

func (s *Server) inetHostView() *hostView {
	h, err := s.st.Host(inetHostID)
	if err != nil {
		return nil
	}
	vs, err := s.views([]*Host{h})
	if err != nil {
		return nil
	}
	return &vs[0]
}

func (s *Server) getInet(w http.ResponseWriter, r *http.Request, _ *User) {
	writeJSON(w, 200, map[string]any{"config": s.inetConfig(), "method": s.inet.prober.Method(), "host": s.inetHostView()})
}

var hostnameRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)

func validAddr(a string) bool {
	if ip := net.ParseIP(a); ip != nil {
		return ip.To4() != nil
	}
	return hostnameRe.MatchString(a)
}

func (s *Server) putInet(w http.ResponseWriter, r *http.Request, u *User) {
	var in InetConfig
	if err := readJSON(r, &in, 8192); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	in.Router = strings.TrimSpace(in.Router)
	if in.Router != "" && !validAddr(in.Router) {
		httpErr(w, 400, "адрес роутера: IP вида 192.168.1.1 или имя")
		return
	}
	if len(in.Targets) == 0 || len(in.Targets) > 5 {
		httpErr(w, 400, "нужно от одного до пяти внешних адресов")
		return
	}
	names := map[string]bool{"роутер": true}
	for i := range in.Targets {
		t := &in.Targets[i]
		t.Name, t.Addr = strings.TrimSpace(t.Name), strings.TrimSpace(t.Addr)
		if !validAddr(t.Addr) {
			httpErr(w, 400, fmt.Sprintf("адрес «%s» не похож на IP или имя сайта", t.Addr))
			return
		}
		if t.Name == "" {
			t.Name = t.Addr
		}
		if len([]rune(t.Name)) > 30 {
			httpErr(w, 400, "название адреса до 30 символов")
			return
		}
		if k := strings.ToLower(t.Name); names[k] {
			httpErr(w, 400, fmt.Sprintf("название «%s» повторяется", t.Name))
			return
		} else {
			names[k] = true
		}
	}
	raw, _ := json.Marshal(in)
	if err := s.st.SetSetting("inet", string(raw)); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	s.inet.reset()
	if in.Enabled {
		s.st.EnsureInetHost()
	}
	if h, err := s.st.Host(inetHostID); err == nil {
		s.st.SetHostMeta(inetHostID, map[string]any{"disabled": !in.Enabled})
		if !in.Enabled {
			h.Meta = map[string]any{"disabled": true}
			s.eng.Eval(h, time.Now()) // закрываем открытые проблемы
		}
	}
	log.Printf("интернет: %s изменил настройки проверки (включена: %v, роутер: %q, адресов: %d)", u.Login, in.Enabled, in.Router, len(in.Targets))
	if in.Enabled {
		select {
		case s.inet.kick <- struct{}{}:
		default:
		}
	}
	writeJSON(w, 200, map[string]any{"config": in})
}

func (s *Server) inetOutages(w http.ResponseWriter, r *http.Request, _ *User) {
	rep, err := s.outages(reportDays(r), time.Now())
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, rep)
}

// inetReport отдаёт журнал простоев файлом CSV: его открывает Excel,
// и его можно приложить к претензии провайдеру.
func (s *Server) inetReport(w http.ResponseWriter, r *http.Request, _ *User) {
	now := time.Now()
	rep, err := s.outages(reportDays(r), now)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	const tf = "02.01.2006 15:04"
	ft := func(u int64) string { return time.Unix(u, 0).In(now.Location()).Format(tf) }
	cfg := s.inetConfig()
	var addrs []string
	for _, t := range cfg.Targets {
		addrs = append(addrs, t.Addr)
	}
	router := cfg.Router
	if router == "" {
		router = "не указан"
	}
	avail := "нет данных"
	if rep.Young {
		avail = "мало данных: проверка работает меньше суток"
	} else if rep.Pct != nil {
		avail = strings.Replace(strconv.FormatFloat(*rep.Pct, 'f', 2, 64), ".", ",", 1) + "%"
	}

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="sentra-internet-%s.csv"`, now.Format("2006-01-02")))
	w.Header().Set("Cache-Control", "no-store")
	w.Write([]byte("\xEF\xBB\xBF")) // чтобы Excel понял кодировку
	cw := csv.NewWriter(w)
	cw.Comma = ';'
	cw.UseCRLF = true
	cw.Write([]string{"Отчёт о недоступности интернета"})
	cw.Write([]string{"Период", ft(rep.From) + " – " + ft(rep.To)})
	cw.Write([]string{"Что проверялось", "раз в 30 секунд: роутер " + router + ", внешние адреса " + strings.Join(addrs, ", ")})
	cw.Write([]string{"Перерывов", strconv.Itoa(len(rep.Outages))})
	cw.Write([]string{"Интернет был недоступен", exactDur(rep.DownSeconds)})
	cw.Write([]string{"Из них роутер отвечал, а внешние адреса нет", exactDur(rep.ProviderSec)})
	cw.Write([]string{"Доступность за период", avail})
	cw.Write([]string{})
	cw.Write([]string{"№", "Начало", "Конец", "Длительность", "Минут", "Причина"})
	for i := len(rep.Outages) - 1; i >= 0; i-- { // в отчёте по порядку: старые сверху
		o := rep.Outages[i]
		end := "продолжается"
		if o.ClosedAt != nil {
			end = ft(*o.ClosedAt)
		}
		cw.Write([]string{strconv.Itoa(len(rep.Outages) - i), ft(o.OpenedAt), end, exactDur(o.Seconds),
			strings.Replace(strconv.FormatFloat(float64(o.Seconds)/60, 'f', 1, 64), ".", ",", 1), causeText[o.Cause]})
	}
	cw.Flush()
}
