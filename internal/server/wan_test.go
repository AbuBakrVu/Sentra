package server

import (
	"bytes"
	"encoding/json"
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

	st.SNMPResult(source, false, "timeout")
	readings := s.inetHostView().WANChannels
	if readings[0].RX != nil || readings[0].TX != nil || readings[1].RX == nil {
		t.Fatal("one failed poll should suppress only its own channel")
	}
	cfg.WANChannels = append(cfg.WANChannels, cfg.WANChannels[0])
	if save() != 400 || len(s.inetConfig().WANChannels) != 2 {
		t.Fatal("a third channel changed the saved configuration")
	}
	cfg.WANChannels = cfg.WANChannels[:2]
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
