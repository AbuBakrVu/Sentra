package server

import (
	"context"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"sentra/internal/snmp"
)

func TestVendorOf(t *testing.T) {
	for oid, want := range map[string]string{
		".1.3.6.1.4.1.14988.1":    "MikroTik",
		"1.3.6.1.4.1.8072.3.2.10": "Linux",
		".1.3.6.1.4.1.99999999.1": "",
		".1.3.6.1.2.1.1":          "",
	} {
		if got := vendorOf(oid); got != want {
			t.Errorf("%s: %q, want %q", oid, got, want)
		}
	}
}

func TestProductOf(t *testing.T) {
	if got := productOf("Ideco NGFW Novum 21.4", ".1.3.6.1.4.1.8072.3.2.10"); got != "Ideco NGFW" {
		t.Errorf("Ideco: %q", got)
	}
	if got := productOf("Linux fw 6.1.0", ".1.3.6.1.4.1.8072.3.2.10"); got != "Linux" {
		t.Errorf("Linux: %q", got)
	}
}

func TestSNMPIfaceProblems(t *testing.T) {
	h := &Host{Kind: kindSNMP, Meta: map[string]any{
		"if_watch": []any{"wan", "lan"},
		"ifaces": []any{
			map[string]any{"name": "wan", "alias": "Ростелеком", "up": false, "admin_up": true},
			map[string]any{"name": "lan", "up": true, "admin_up": true},
			map[string]any{"name": "port7", "up": false, "admin_up": true}, // никогда не поднимался
			map[string]any{"name": "dmz", "up": false, "admin_up": false},  // выключен намеренно
		},
	}}
	want := map[string]desired{}
	snmpIfaceProblems(h, want)
	if len(want) != 1 || want["if:wan"].title != "Интерфейс wan (Ростелеком) не на связи" || want["if:wan"].sev != 1 {
		t.Errorf("%+v", want)
	}
}

// Сквозной опрос настоящего агента: SENTRA_SNMP_TEST=127.0.0.1:1161 (см. internal/snmp/live_test.go).
func TestCollectSNMPLive(t *testing.T) {
	addr := os.Getenv("SENTRA_SNMP_TEST")
	if addr == "" {
		t.Skip("SENTRA_SNMP_TEST не задан")
	}
	fvm := &fakeVM{}
	vmSrv := httptest.NewServer(fvm)
	defer vmSrv.Close()
	st := testStore(t)
	s := &Server{st: st, vm: NewVM(vmSrv.URL), snmp: newSNMPPoller(), eng: NewEngine(st)}
	ctx := context.Background()
	a, _, err := snmp.Probe(ctx, addr, snmp.Auth{Version: "3", User: "sentra", AuthPass: "authpass123", PrivPass: "privpass123"})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := st.AddSNMP("", addr, a)
	d, _ := st.SNMPDevice(id)
	for i := 0; i < 2; i++ { // второй опрос даёт скорость интерфейсов
		if err := s.collectSNMP(ctx, d); err != nil {
			t.Fatal(err)
		}
		time.Sleep(1100 * time.Millisecond)
	}
	hs, _ := st.Hosts()
	if len(hs) != 1 || hs[0].Kind != kindSNMP || hs[0].Name != "ideco-test" {
		t.Fatalf("%+v", hs)
	}
	h := hs[0]
	t.Logf("метрики: %v", h.LastMetrics)
	t.Logf("интерфейсы: %v, следим: %v", h.Meta["ifaces"], h.Meta["if_watch"])
	if h.LastMetrics["mem_used_percent"] == nil || h.LastMetrics["fs"] == nil {
		t.Error("нет памяти или дисков")
	}
	if !fvm.has("sentra_net_rx_bytes_per_sec") {
		t.Error("нет скорости интерфейсов")
	}
	views, _ := s.views(hs)
	if views[0].Status == "offline" || views[0].Status == "critical" {
		t.Errorf("статус %s %v", views[0].Status, views[0].Issues)
	}
}
