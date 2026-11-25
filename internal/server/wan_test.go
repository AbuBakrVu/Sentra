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
	s := &Server{st: st}
	cfg := defaultInetConfig()
	cfg.WAN = &WANConfig{Host: id, Interface: "ppp0", Download: 100, Upload: 20}
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
		t.Fatal("could not save WAN binding")
	}
	if err := st.EnsureInetHost(); err != nil {
		t.Fatal(err)
	}
	v := s.inetHostView()
	if v == nil || v.WAN == nil || v.WAN.Download != 100 || v.WAN.Upload != 20 || v.WAN.RX == nil || *v.WAN.RX != 0 || v.WAN.TX == nil || *v.WAN.TX != 1250000 {
		t.Fatalf("missing WAN snapshot: %+v", v)
	}
	st.SNMPResult(source, false, "timeout")
	if v := s.inetHostView().WAN; v.RX != nil || v.TX != nil {
		t.Fatal("stale traffic exposed")
	}
	cfg.WAN.Interface = "missing"
	if save() != 400 || s.inetConfig().WAN.Interface != "ppp0" {
		t.Fatal("invalid port replaced binding")
	}
	cfg.WAN = nil
	if save() != 200 || s.inetHostView().WAN != nil {
		t.Fatal("WAN binding was not removed")
	}
}
