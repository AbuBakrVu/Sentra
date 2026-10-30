package server

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"text/template"
	"time"

	"sentra/internal/proto"
)

type Config struct {
	Listen    string // :443
	DataDir   string // /var/lib/sentra
	VMURL     string // http://victoriametrics:8428
	PublicURL string // https://192.168.1.10 (если пусто — берём из запроса)
	AgentsDir string // каталог с бинарниками агентов
	CertHosts []string
	AdminPass string // пароль первого администратора (если пусто — сгенерируем)
	Version   string
}

type Server struct {
	cfg        Config
	st         *Store
	vm         *VM
	pin        string
	web        fs.FS
	loginMu    sync.Mutex
	loginFails map[string][]time.Time
}

const offlineAfter = 3 * time.Minute

func New(cfg Config, web fs.FS) (*Server, *tls.Certificate, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, nil, err
	}
	st, err := OpenStore(filepath.Join(cfg.DataDir, "sentra.db"))
	if err != nil {
		return nil, nil, fmt.Errorf("база данных: %w", err)
	}
	cert, pin, err := LoadOrCreateCert(cfg.DataDir, cfg.CertHosts)
	if err != nil {
		return nil, nil, fmt.Errorf("сертификат: %w", err)
	}
	if st.UserCount() == 0 {
		pw := cfg.AdminPass
		if pw == "" {
			pw = randToken(12)
		}
		if err := st.CreateUser("admin", pw, "admin"); err != nil {
			return nil, nil, err
		}
		pf := filepath.Join(cfg.DataDir, "initial-admin-password.txt")
		os.WriteFile(pf, []byte(pw+"\n"), 0o600)
		log.Printf("Создан администратор: логин admin, пароль %s (сохранён в %s). Смените его после входа.", pw, pf)
	}
	s := &Server{cfg: cfg, st: st, vm: NewVM(cfg.VMURL), pin: pin, web: web, loginFails: map[string][]time.Time{}}
	return s, &cert, nil
}

func (s *Server) Pin() string { return s.pin }

func (s *Server) Handler() http.Handler {
	m := http.NewServeMux()
	// агент
	m.HandleFunc("POST /api/agent/register", s.agentRegister)
	m.HandleFunc("POST /api/agent/metrics", s.agentMetrics)
	m.HandleFunc("GET /install.sh", s.installScript)
	m.HandleFunc("GET /download/{file}", s.download)
	// интерфейс
	m.HandleFunc("POST /api/login", s.login)
	m.HandleFunc("POST /api/logout", s.logout)
	m.HandleFunc("GET /api/me", s.auth(s.me))
	m.HandleFunc("POST /api/password", s.auth(s.changePassword))
	m.HandleFunc("GET /api/hosts", s.auth(s.listHosts))
	m.HandleFunc("GET /api/hosts/{id}", s.auth(s.getHost))
	m.HandleFunc("PATCH /api/hosts/{id}", s.auth(s.renameHost))
	m.HandleFunc("DELETE /api/hosts/{id}", s.auth(s.deleteHost))
	m.HandleFunc("GET /api/hosts/{id}/chart", s.auth(s.chart))
	m.HandleFunc("POST /api/enroll", s.auth(s.newEnroll))
	m.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true, "version": s.cfg.Version})
	})
	m.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { httpErr(w, 404, "не найдено") })
	m.HandleFunc("/", s.static)
	return secHeaders(m)
}

func secHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		h.ServeHTTP(w, r)
	})
}

// ---------- вспомогательное ----------

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func readJSON(r *http.Request, v any, limit int64) error {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return errors.New("ожидается application/json")
	}
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, limit)).Decode(v)
}

func clientIP(r *http.Request) string {
	h, _, _ := net.SplitHostPort(r.RemoteAddr)
	return h
}

func (s *Server) publicURL(r *http.Request) string {
	if s.cfg.PublicURL != "" {
		return strings.TrimRight(s.cfg.PublicURL, "/")
	}
	return "https://" + r.Host
}

// ---------- авторизация ----------

type ctxKey struct{}

const cookieName = "sentra_session"

func (s *Server) auth(next func(http.ResponseWriter, *http.Request, *User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(cookieName)
		if err != nil {
			httpErr(w, 401, "требуется вход")
			return
		}
		u, err := s.st.SessionUser(c.Value)
		if err != nil {
			httpErr(w, 401, "сессия истекла")
			return
		}
		// защита от CSRF: изменяющие запросы только с JSON и с нашего адреса
		if r.Method != "GET" {
			if o := r.Header.Get("Origin"); o != "" && !strings.HasSuffix(o, "://"+r.Host) {
				httpErr(w, 403, "запрос с чужого адреса")
				return
			}
		}
		next(w, r, u)
	}
}

func (s *Server) tooManyFails(ip string) bool {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	var recent []time.Time
	for _, t := range s.loginFails[ip] {
		if time.Since(t) < 15*time.Minute {
			recent = append(recent, t)
		}
	}
	s.loginFails[ip] = recent
	return len(recent) >= 10
}

func (s *Server) addFail(ip string) {
	s.loginMu.Lock()
	s.loginFails[ip] = append(s.loginFails[ip], time.Now())
	s.loginMu.Unlock()
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if s.tooManyFails(ip) {
		httpErr(w, 429, "слишком много попыток, подождите 15 минут")
		return
	}
	var in struct{ Login, Password string }
	if err := readJSON(r, &in, 4096); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	u, err := s.st.Authenticate(strings.TrimSpace(in.Login), in.Password)
	if err != nil {
		s.addFail(ip)
		time.Sleep(400 * time.Millisecond)
		httpErr(w, 401, err.Error())
		return
	}
	tok, err := s.st.NewSession(u.ID)
	if err != nil {
		httpErr(w, 500, "не удалось создать сессию")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: tok, Path: "/", HttpOnly: true, Secure: true,
		SameSite: http.SameSiteStrictMode, MaxAge: int(sessionTTL.Seconds())})
	writeJSON(w, 200, u)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		s.st.DeleteSession(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request, u *User) { writeJSON(w, 200, u) }

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request, u *User) {
	var in struct{ Old, New string }
	if err := readJSON(r, &in, 4096); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	if _, err := s.st.Authenticate(u.Login, in.Old); err != nil {
		httpErr(w, 400, "текущий пароль неверный")
		return
	}
	if len([]rune(in.New)) < 8 {
		httpErr(w, 400, "новый пароль должен быть не короче 8 символов")
		return
	}
	if err := s.st.SetPassword(u.ID, in.New); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	os.Remove(filepath.Join(s.cfg.DataDir, "initial-admin-password.txt"))
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---------- агент ----------

func (s *Server) agentRegister(w http.ResponseWriter, r *http.Request) {
	var in proto.RegisterRequest
	if err := readJSON(r, &in, 64<<10); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	if !s.st.UseEnrollToken(in.Token) {
		httpErr(w, 403, "токен подключения недействителен или истёк, создайте новый в панели")
		return
	}
	id, key, err := s.st.RegisterHost(in.Inventory)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	log.Printf("подключён хост %s (%s, %s) с %s", in.Inventory.Hostname, id, in.Inventory.OSName, clientIP(r))
	writeJSON(w, 200, proto.RegisterResponse{HostID: id, Key: key})
}

func (s *Server) agentMetrics(w http.ResponseWriter, r *http.Request) {
	id, key, ok := strings.Cut(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), ":")
	if !ok || !s.st.CheckHostKey(id, key) {
		httpErr(w, 401, "неизвестный агент")
		return
	}
	var b proto.MetricsBatch
	if err := readJSON(r, &b, 2<<20); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	now := time.Now().UnixMilli()
	// часы агента могут уйти — не даём записать точку из будущего или далёкого прошлого
	if b.Timestamp > now+60_000 || b.Timestamp < now-3_600_000 {
		b.Timestamp = now
	}
	if err := s.vm.Import(r.Context(), id, b.Timestamp, b.Samples); err != nil {
		log.Printf("запись метрик хоста %s: %v", id, err)
		httpErr(w, 503, "хранилище метрик недоступно")
		return
	}
	if err := s.st.TouchHost(id, time.Now().Unix(), summarize(b.Samples), b.Inventory); err != nil {
		log.Printf("обновление хоста %s: %v", id, err)
	}
	w.WriteHeader(204)
}

// summarize сворачивает пакет в несколько чисел для плиток на обзоре.
func summarize(ss []proto.Sample) map[string]any {
	m := map[string]any{}
	var rx, tx, diskMax float64
	diskMount := ""
	for _, s := range ss {
		switch s.Name {
		case proto.MCPUUsage, proto.MMemUsedPct, proto.MLoad1, proto.MUptime, proto.MSwapUsedPct, proto.MMemTotal, proto.MMemUsed:
			m[s.Name] = s.Value
		case proto.MFsUsedPct:
			if s.Value >= diskMax {
				diskMax, diskMount = s.Value, s.Labels["mount"]
			}
		case proto.MNetRx:
			rx += s.Value
		case proto.MNetTx:
			tx += s.Value
		}
	}
	m["disk_max_percent"] = diskMax
	m["disk_max_mount"] = diskMount
	m["net_rx"] = rx
	m["net_tx"] = tx
	return m
}

func num(m map[string]any, k string) float64 {
	f, _ := m[k].(float64)
	return f
}

var installTpl = template.Must(template.New("i").Parse(`#!/bin/sh
# Установка агента мониторинга Sentra
set -eu
SERVER='{{.Server}}'
TOKEN='{{.Token}}'
PIN='{{.Pin}}'

if [ "$(id -u)" -ne 0 ]; then echo "Запустите от root: ... | sudo sh" >&2; exit 1; fi
case "$(uname -s)" in Linux) ;; *) echo "Этот скрипт для Linux" >&2; exit 1;; esac
case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) echo "Архитектура $(uname -m) пока не поддерживается" >&2; exit 1 ;;
esac
command -v systemctl >/dev/null 2>&1 || { echo "Нужен systemd" >&2; exit 1; }

echo "Скачиваю агент ($ARCH)..."
TMP=$(mktemp)
trap 'rm -f "$TMP"' EXIT
curl -fsSk --pinnedpubkey "sha256//$PIN" -o "$TMP" "$SERVER/download/sentra-agent-linux-$ARCH"
systemctl stop sentra-agent >/dev/null 2>&1 || true
install -m 0755 "$TMP" /usr/local/bin/sentra-agent

echo "Регистрирую на сервере..."
/usr/local/bin/sentra-agent enroll --server "$SERVER" --token "$TOKEN" --pin "$PIN"

cat > /etc/systemd/system/sentra-agent.service <<'UNIT'
[Unit]
Description=Sentra monitoring agent
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/sentra-agent run
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
UNIT

systemctl daemon-reload
systemctl enable --now sentra-agent >/dev/null 2>&1
echo "Готово. Через минуту сервер появится в панели $SERVER"
`))

var tokenRe = regexp.MustCompile(`^[A-Za-z0-9_-]{10,64}$`)

func (s *Server) installScript(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("t")
	if !tokenRe.MatchString(tok) || !s.st.EnrollTokenValid(tok) {
		// 200, а не 403: иначе curl -f промолчит и человек не увидит причину
		w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
		fmt.Fprintln(w, `echo "Токен подключения недействителен или истёк. Создайте новую команду в панели." >&2; exit 1`)
		return
	}
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	installTpl.Execute(w, map[string]string{"Server": s.publicURL(r), "Token": tok, "Pin": s.pin})
}

var agentFileRe = regexp.MustCompile(`^sentra-agent-(linux|windows)-(amd64|arm64)(\.exe)?$`)

func (s *Server) download(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	if !agentFileRe.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	p := filepath.Join(s.cfg.AgentsDir, name)
	if _, err := os.Stat(p); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeFile(w, r, p)
}

// ---------- хосты ----------

type hostView struct {
	*Host
	Online bool     `json:"online"`
	Status string   `json:"status"` // ok, warning, critical, offline, waiting
	Issues []string `json:"issues"`
}

func view(h *Host) hostView {
	v := hostView{Host: h, Issues: []string{}}
	if h.LastSeen == 0 {
		v.Status = "waiting"
		return v
	}
	v.Online = time.Since(time.Unix(h.LastSeen, 0)) < offlineAfter
	if !v.Online {
		v.Status = "offline"
		v.Issues = append(v.Issues, "Нет данных от агента")
		return v
	}
	// стартовые пороги из ТЗ; настраиваемый движок появится на этапе 2
	v.Status = "ok"
	check := func(val float64, warn, crit float64, what string) {
		switch {
		case val > crit:
			v.Status = "critical"
			v.Issues = append(v.Issues, fmt.Sprintf("%s %.0f%%", what, val))
		case val > warn:
			if v.Status == "ok" {
				v.Status = "warning"
			}
			v.Issues = append(v.Issues, fmt.Sprintf("%s %.0f%%", what, val))
		}
	}
	m := h.LastMetrics
	check(num(m, proto.MCPUUsage), 85, 95, "CPU")
	check(num(m, proto.MMemUsedPct), 85, 95, "Память")
	disk := "Диск"
	if mnt, _ := m["disk_max_mount"].(string); mnt != "" {
		disk = "Диск " + mnt
	}
	check(num(m, "disk_max_percent"), 85, 95, disk)
	return v
}

func (s *Server) listHosts(w http.ResponseWriter, r *http.Request, _ *User) {
	hs, err := s.st.Hosts()
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	out := make([]hostView, 0, len(hs))
	for _, h := range hs {
		out = append(out, view(h))
	}
	writeJSON(w, 200, out)
}

func (s *Server) getHost(w http.ResponseWriter, r *http.Request, _ *User) {
	h, err := s.st.Host(r.PathValue("id"))
	if err != nil {
		httpErr(w, 404, "хост не найден")
		return
	}
	writeJSON(w, 200, view(h))
}

func (s *Server) renameHost(w http.ResponseWriter, r *http.Request, _ *User) {
	var in struct{ Name string }
	if err := readJSON(r, &in, 4096); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || len([]rune(name)) > 100 {
		httpErr(w, 400, "имя от 1 до 100 символов")
		return
	}
	if err := s.st.RenameHost(r.PathValue("id"), name); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) deleteHost(w http.ResponseWriter, r *http.Request, _ *User) {
	if err := s.st.DeleteHost(r.PathValue("id")); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) newEnroll(w http.ResponseWriter, r *http.Request, _ *User) {
	tok, exp, err := s.st.NewEnrollToken()
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	base := s.publicURL(r)
	cmd := fmt.Sprintf("curl -fsSk --pinnedpubkey 'sha256//%s' '%s/install.sh?t=%s' | sudo sh", s.pin, base, tok)
	writeJSON(w, 200, map[string]any{"token": tok, "expires_at": exp.Unix(), "linux": cmd, "server": base})
}

// ---------- графики ----------

var ranges = map[string]struct{ span, step time.Duration }{
	"1h":  {time.Hour, 30 * time.Second},
	"6h":  {6 * time.Hour, 2 * time.Minute},
	"24h": {24 * time.Hour, 5 * time.Minute},
	"7d":  {7 * 24 * time.Hour, 30 * time.Minute},
	"30d": {30 * 24 * time.Hour, 2 * time.Hour},
}

type chartQuery struct {
	metric string
	legend func(map[string]string) string
}

func fixed(s string) func(map[string]string) string {
	return func(map[string]string) string { return s }
}

func (s *Server) chart(w http.ResponseWriter, r *http.Request, _ *User) {
	id := r.PathValue("id")
	if _, err := s.st.Host(id); err != nil {
		httpErr(w, 404, "хост не найден")
		return
	}
	rg, ok := ranges[r.URL.Query().Get("range")]
	if !ok {
		rg = ranges["1h"]
	}
	var qs []chartQuery
	unit := "%"
	switch r.URL.Query().Get("m") {
	case "cpu":
		qs = []chartQuery{{proto.MCPUUsage, fixed("Загрузка")}, {proto.MCPUIOWait, fixed("Ожидание диска (iowait)")}}
	case "mem":
		qs = []chartQuery{{proto.MMemUsedPct, fixed("Память")}, {proto.MSwapUsedPct, fixed("Swap")}}
	case "load":
		unit = ""
		qs = []chartQuery{{proto.MLoad1, fixed("1 мин")}, {proto.MLoad5, fixed("5 мин")}, {proto.MLoad15, fixed("15 мин")}}
	case "disk":
		qs = []chartQuery{{proto.MFsUsedPct, func(l map[string]string) string { return l["mount"] }}}
	case "net":
		unit = "B/s"
		qs = []chartQuery{
			{proto.MNetRx, func(l map[string]string) string { return l["iface"] + " приём" }},
			{proto.MNetTx, func(l map[string]string) string { return l["iface"] + " передача" }},
		}
	default:
		httpErr(w, 400, "неизвестный график")
		return
	}
	end := time.Now()
	start := end.Add(-rg.span)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	var out *Chart
	for _, q := range qs {
		expr := fmt.Sprintf(`avg_over_time(%s%s{host=%q}[%ds])`, metricPrefix, q.metric, id, int(math.Max(rg.step.Seconds(), 60)))
		ch, err := s.vm.QueryRange(ctx, expr, start, end, rg.step, q.legend)
		if err != nil {
			httpErr(w, 502, err.Error())
			return
		}
		if out == nil {
			out = ch
		} else {
			out.Series = append(out.Series, ch.Series...)
		}
	}
	out.Unit = unit
	if out.Series == nil {
		out.Series = []Series{}
	}
	writeJSON(w, 200, out)
}

// ---------- статика интерфейса ----------

func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/")
	if p == "" {
		p = "index.html"
	}
	if f, err := s.web.Open(p); err == nil {
		st, _ := f.Stat()
		f.Close()
		if !st.IsDir() {
			if strings.HasPrefix(p, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			http.ServeFileFS(w, r, s.web, p)
			return
		}
	}
	// SPA: все остальные пути отдаём index.html
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFileFS(w, r, s.web, "index.html")
}
