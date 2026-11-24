package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sentra/internal/netprobe"
)

func TestIPMonitoring(t *testing.T) {
	st := testStore(t)
	s := &Server{st: st, eng: NewEngine(st)}
	add := func(addr string, code int) string {
		t.Helper()
		res := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/ip", strings.NewReader(`{"addr":"`+addr+`"}`))
		req.Header.Set("Content-Type", "application/json")
		s.addIP(res, req, nil)
		if res.Code != code {
			t.Fatalf("%s: %d: %s", addr, res.Code, res.Body)
		}
		var body struct{ ID string }
		json.Unmarshal(res.Body.Bytes(), &body)
		return body.ID
	}
	add("192.0.2.0/24", 400)
	id := add("192.0.2.15", 201)
	add("192.0.2.15", 409)
	check := func(id, want string) *Host {
		t.Helper()
		h, err := st.Host(id)
		if err != nil {
			t.Fatal(err)
		}
		open, err := st.OpenProblemsForHost(id)
		if err != nil {
			t.Fatal(err)
		}
		if v := view(h, open); v.Status != want {
			t.Fatalf("%s: got %s, want %s", id, v.Status, want)
		}
		return h
	}
	check(id, "waiting")
	lastSNMP := time.Now().Add(-10 * time.Minute).Unix()
	snmpID, err := st.UpsertPVEHost("test-snmp", "dev", kindSNMP, "Switch", lastSNMP, nil, map[string]any{"addr": "192.0.2.22:1161"})
	if err != nil {
		t.Fatal(err)
	}
	poll := func(ipReplies bool) {
		s.pollPingHosts(context.Background(), func(_ context.Context, addr string) (netprobe.Result, error) {
			r := netprobe.Result{Sent: 2}
			if addr == "192.0.2.22" || ipReplies {
				r.Recv = 2
				r.AvgMs = 3
			}
			return r, nil
		})
		s.eng.Sweep(time.Now())
	}
	poll(false)
	h := check(id, "offline")
	if h.LastSeen != 0 {
		t.Fatal("timeout set last successful reply")
	}
	if probs, _ := st.OpenProblemsForHost(id); len(probs) != 1 || probs[0].Kind != "down" {
		t.Fatalf("missing outage: %+v", probs)
	}
	h = check(snmpID, "warning")
	if h.LastSeen != lastSNMP || h.Ping == nil || h.Ping.State != "up" {
		t.Fatalf("ICMP overwrote SNMP freshness: %+v", h)
	}
	if probs, _ := st.OpenProblemsForHost(snmpID); len(probs) != 1 || probs[0].Kind != "degraded" {
		t.Fatalf("SNMP-only failure counted as downtime: %+v", probs)
	}
	poll(true)
	h = check(id, "ok")
	if h.LastSeen == 0 || h.Ping.RTT != 3 {
		t.Fatal("successful ping not stored")
	}
	if probs, _ := st.OpenProblemsForHost(id); len(probs) != 0 {
		t.Fatal("outage still open after recovery")
	}
	if err := st.DeleteHost(id); err != nil {
		t.Fatal(err)
	}
	poll(true)
	add("192.0.2.15", 201)
}
