package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"sentra/internal/snmp"
)

func TestWANSelectionAndFreshRates(t *testing.T) {
	st := testStore(t)
	source, err := st.AddSNMP("gateway", "192.0.2.1:161", snmp.Auth{Version: "2c"})
	if err != nil {
		t.Fatal(err)
	}
	port := map[string]any{"name": "ppp0", "up": true, "rx": 0, "tx": 1250000}
	id, err := st.UpsertPVEHost(source, "dev", kindSNMP, "gateway", time.Now().Unix(), nil, map[string]any{"ifaces": []any{port}})
	if err != nil {
		t.Fatal(err)
	}
	secondSource, err := st.AddSNMP("PIX", "192.0.2.2:161", snmp.Auth{Version: "2c"})
	if err != nil {
		t.Fatal(err)
	}
	secondPort := map[string]any{"name": "internet", "up": true, "rx": 2500000, "tx": 0}
	secondID, err := st.UpsertPVEHost(secondSource, "dev", kindSNMP, "PIX", time.Now().Unix(), nil, map[string]any{"ifaces": []any{secondPort}})
	if err != nil {
		t.Fatal(err)
	}

	s := &Server{st: st}
	cfg := defaultInetConfig()
	cfg.Enabled = false // SNMP-трафик виден и при выключенной проверке выхода в интернет.
	cfg.WAN = &WANConfig{Host: id, Interface: "ppp0", Download: 100, Upload: 20}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting("inet", string(raw)); err != nil {
		t.Fatal(err)
	}
	cfg = s.inetConfig()
	if cfg.WAN != nil || len(cfg.WANChannels) != 1 || cfg.WANChannels[0].Host != id || cfg.WANChannels[0].Download != 100 {
		t.Fatalf("legacy configuration was not migrated: %+v", cfg)
	}
	cfg.WANChannels = append(cfg.WANChannels, WANConfig{Name: "PIX", Host: secondID, Interface: "internet", Download: 80, Upload: 30})
	save := func() int {
		data, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		res := httptest.NewRecorder()
		req := httptest.NewRequest("PUT", "/api/inet", bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		s.putInet(res, req, &User{})
		return res.Code
	}
	if save() != 200 {
		t.Fatal("could not save two WAN channels")
	}
	v := s.inetHostView()
	if v == nil || v.Status != "stopped" || len(v.WANChannels) != 2 {
		t.Fatalf("missing WAN channels: %+v", v)
	}
	first, second := v.WANChannels[0], v.WANChannels[1]
	if first.Download != 100 || first.Upload != 20 || first.RX == nil || *first.RX != 0 || first.TX == nil || *first.TX != 1250000 {
		t.Fatalf("incorrect first channel: %+v", first)
	}
	if second.Name != "PIX" || second.Download != 80 || second.RX == nil || *second.RX != 2500000 || second.TX == nil || *second.TX != 0 {
		t.Fatalf("incorrect second channel: %+v", second)
	}

	asaSource, err := st.AddSNMP("Cisco ASA", "192.0.2.3:161", snmp.Auth{Version: "2c"})
	if err != nil {
		t.Fatal(err)
	}
	asaPort := map[string]any{"name": "outside", "up": true, "rx": 1000000, "tx": 250000}
	asaID, err := st.UpsertPVEHost(asaSource, "dev", kindSNMP, "Cisco ASA", time.Now().Unix(), nil, map[string]any{"ifaces": []any{asaPort}})
	if err != nil {
		t.Fatal(err)
	}
	cfg.WANChannels = append(cfg.WANChannels, WANConfig{Name: "Cisco ASA", Host: asaID, Interface: "outside", Download: 100, Upload: 50})
	if save() != 200 {
		t.Fatal("could not add the third WAN channel to an existing setup")
	}
	if got := s.inetHostView().WANChannels; len(got) != 3 || got[0].Host != id || got[1].Host != secondID ||
		got[2].Host != asaID || got[2].RX == nil || *got[2].RX != 1000000 {
		t.Fatalf("expected the original pair and the ASA channel: %+v", got)
	}

	st.SNMPResult(source, false, "timeout")
	readings := s.inetHostView().WANChannels
	if readings[0].RX != nil || readings[0].TX != nil || readings[1].RX == nil || readings[2].RX == nil {
		t.Fatal("one failed poll should suppress only its own channel")
	}
	cfg.WANChannels = append(cfg.WANChannels, cfg.WANChannels[0])
	if save() != 400 || len(s.inetConfig().WANChannels) != 3 {
		t.Fatal("a fourth channel changed the saved configuration")
	}
	cfg.WANChannels = cfg.WANChannels[:3]
	cfg.WANChannels[1] = cfg.WANChannels[0]
	if save() != 400 || s.inetConfig().WANChannels[1].Host != secondID {
		t.Fatal("duplicate channel replaced the saved configuration")
	}
	cfg.WANChannels[1].Interface = "missing"
	if save() != 400 || s.inetConfig().WANChannels[1].Host != secondID {
		t.Fatal("invalid port replaced the saved configuration")
	}
	cfg.WANChannels = []WANConfig{}
	if save() != 200 || len(s.inetHostView().WANChannels) != 0 {
		t.Fatal("WAN channels were not removed")
	}
}

func TestLiveWANPollsSelectedPorts(t *testing.T) {
	st := testStore(t)
	s := &Server{st: st, inet: newInetPoller()}
	cfg := defaultInetConfig()
	cfg.Enabled = false
	for i, addr := range []string{"192.0.2.1:161", "192.0.2.2:161", "192.0.2.3:161"} {
		source, err := st.AddSNMP("Шлюз", addr, snmp.Auth{Version: "2c"})
		if err != nil {
			t.Fatal(err)
		}
		ports := []any{
			map[string]any{"index": 1, "name": "lan"},
			map[string]any{"index": i + 2, "name": "wan"},
		}
		id, err := st.UpsertPVEHost(source, "dev", kindSNMP, "Шлюз", time.Now().Unix(), nil, map[string]any{"ifaces": ports})
		if err != nil {
			t.Fatal(err)
		}
		cfg.WANChannels = append(cfg.WANChannels, WANConfig{Host: id, Interface: "wan", Download: 100})
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting("inet", string(raw)); err != nil {
		t.Fatal(err)
	}
	readings := func() []wanReading {
		response := httptest.NewRecorder()
		s.getWAN(response, httptest.NewRequest("GET", "/api/inet/wan", nil), &User{})
		var out []wanReading
		if err := json.Unmarshal(response.Body.Bytes(), &out); err != nil || len(out) != 3 {
			t.Fatalf("invalid WAN response: %s (%v)", response.Body, err)
		}
		return out
	}
	uptime := uint32(10000)
	var firstIn uint64 = 1000
	var secondUnavailable bool
	poll := func(_ context.Context, addr string, _ snmp.Auth, index int) (snmp.Iface, uint32, error) {
		f := snmp.Iface{Index: index, Name: "wan", OperStatus: 1, Counters: true, HC: true, Speed: 100e6}
		switch {
		case addr == "192.0.2.1:161" && index == 2:
			f.In = firstIn
		case addr == "192.0.2.2:161" && index == 3:
			if secondUnavailable {
				return snmp.Iface{}, 0, errors.New("timeout")
			}
		case addr == "192.0.2.3:161" && index == 4:
		default:
			t.Errorf("polled unselected interface %s/%d", addr, index)
			return snmp.Iface{}, 0, errors.New("wrong interface")
		}
		return f, uptime, nil
	}

	s.wanCycle(context.Background(), poll)
	if got := readings(); got[0].MeasuredAt != 0 || got[0].RX != nil || got[1].RX != nil {
		t.Fatal("first sample must establish a baseline")
	}
	firstIn += 100
	s.wanCycle(context.Background(), poll)
	if got := readings(); got[0].MeasuredAt == 0 || got[0].RX == nil || *got[0].RX <= 0 || got[1].RX == nil || *got[1].RX != 0 {
		t.Fatalf("expected live traffic and a distinct zero sample: %+v", got)
	}
	secondUnavailable = true
	s.wanCycle(context.Background(), poll)
	if got := readings(); got[0].RX == nil || got[1].RX != nil || got[1].MeasuredAt != 0 || got[2].RX == nil || got[2].MeasuredAt == 0 {
		t.Fatal("missing poll should invalidate only that interface")
	}
	uptime = 100
	s.wanCycle(context.Background(), poll)
	if got := readings(); got[0].RX != nil || got[0].MeasuredAt != 0 {
		t.Fatal("counter rates must be suppressed on restart")
	}
}
