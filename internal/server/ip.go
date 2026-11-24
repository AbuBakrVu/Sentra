package server

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"sentra/internal/netprobe"
)

const kindPing = "ping"

type PingResult struct {
	State     string  `json:"state"`
	Checked   int64   `json:"checked_at"`
	RTT       float64 `json:"rtt_ms"`
	LastReply int64   `json:"last_reply,omitempty"`
}

// Если SNMP устарел, свежий ответ на ping отличает потерю метрик от потери связи.
func probeCondition(h *Host, now time.Time) (string, string) {
	p := h.Ping
	fresh := p != nil && now.Sub(time.Unix(p.Checked, 0)) < offlineAfter
	if h.Kind == kindSNMP && h.LastSeen > 0 && now.Sub(time.Unix(h.LastSeen, 0)) >= offlineAfter && fresh && p.State == "up" {
		return "warning", "Нет данных SNMP, устройство отвечает на ping"
	}
	if h.Kind != kindPing {
		return "", ""
	}
	if !fresh {
		return "waiting", "Ожидается проверка по IP"
	}
	switch p.State {
	case "up":
		return "ok", ""
	case "down":
		return "offline", "Нет ответа на ping"
	default:
		return "waiting", "Сервер мониторинга не смог выполнить ping"
	}
}

func (s *Server) addIP(w http.ResponseWriter, r *http.Request, _ *User) {
	var in struct {
		Addr string `json:"addr"`
		Name string `json:"name"`
	}
	if err := readJSON(r, &in, 2048); err != nil {
		httpErr(w, 400, "Проверьте адрес и название")
		return
	}
	addr, err := netip.ParseAddr(strings.TrimSpace(in.Addr))
	if err != nil || !addr.Is4() || addr.IsMulticast() || addr.IsUnspecified() || addr.String() == "255.255.255.255" {
		httpErr(w, 400, "Укажите IPv4-адрес одного устройства")
		return
	}
	name := strings.TrimSpace(in.Name)
	if len([]rune(name)) > 100 {
		httpErr(w, 400, "Название до 100 символов")
		return
	}
	if name == "" {
		name = addr.String()
	}
	id := "ip-" + strings.ToLower(randToken(12))
	meta, _ := json.Marshal(map[string]string{"addr": addr.String()})
	res, err := s.st.db.Exec(`INSERT INTO hosts(id,name,key_hash,kind,source_id,ext_id,meta,created_at)
 SELECT ?,?,'-','ping','ping',?,?,? WHERE NOT EXISTS (SELECT 1 FROM snmp_devices WHERE addr LIKE ?)
 ON CONFLICT(source_id,ext_id) WHERE source_id!='' DO NOTHING`, id, name, addr.String(), string(meta), time.Now().Unix(), addr.String()+":%")
	if err != nil {
		httpErr(w, 500, "Не удалось сохранить устройство")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		httpErr(w, 409, "Этот адрес уже проверяется по ping")
		return
	}
	writeJSON(w, 201, map[string]string{"id": id, "name": name})
}

func (s *Server) pollPing(ctx context.Context) {
	ticker := time.NewTicker(pvePollEvery)
	defer ticker.Stop()
	for ctx.Err() == nil {
		s.pollPingHosts(ctx, s.inet.prober.Echo)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) pollPingHosts(ctx context.Context, echo func(context.Context, string) (netprobe.Result, error)) {
	rows, err := s.st.db.QueryContext(ctx, `SELECT id,kind,json_extract(meta,'$.addr'),ping_result FROM hosts WHERE kind IN ('ping','snmp') AND json_extract(meta,'$.addr') IS NOT NULL`)
	if err != nil {
		log.Printf("ping: %v", err)
		return
	}
	type target struct{ id, kind, addr, previous string }
	var targets []target
	for rows.Next() {
		var t target
		if err = rows.Scan(&t.id, &t.kind, &t.addr, &t.previous); err != nil {
			break
		}
		targets = append(targets, t)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		log.Printf("ping: %v", err)
		return
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 16)
	for _, t := range targets {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return
		}
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done() }()
			addr := t.addr
			if t.kind == kindSNMP {
				var err error
				addr, _, err = net.SplitHostPort(addr)
				if err != nil {
					return
				}
			}
			result, probeErr := echo(ctx, addr)
			if ctx.Err() != nil {
				return
			}
			var p PingResult
			json.Unmarshal([]byte(t.previous), &p)
			p.Checked = time.Now().Unix()
			p.RTT = result.AvgMs
			switch {
			case probeErr != nil:
				p.State = "error"
			case result.Up():
				p.State = "up"
				p.LastReply = p.Checked
			default:
				p.State = "down"
			}
			data, _ := json.Marshal(p)
			_, err := s.st.db.ExecContext(ctx, `UPDATE hosts SET ping_result=?,last_seen=CASE WHEN kind='ping' THEN ? ELSE last_seen END WHERE id=? AND json_extract(meta,'$.addr')=?`, string(data), p.LastReply, t.id, t.addr)
			if err != nil {
				log.Printf("ping %s: %v", t.id, err)
				return
			}
			if h, err := s.st.Host(t.id); err == nil && h.Kind == kindPing {
				s.eng.Eval(h, time.Now())
			}
		}()
	}
	wg.Wait()
}
