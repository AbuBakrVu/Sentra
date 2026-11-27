package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
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

func TestSNMPIfaceRates(t *testing.T) {
	prev := snmp.Iface{Name: "eth6", Speed: 1e9, In: 1<<32 - 20, Out: 100, Counters: true}
	cur := prev
	cur.In, cur.Out = 80, 100
	if rx, tx, ok := snmpIfaceRates(prev, cur, 10); !ok || rx != 10 || tx != 0 {
		t.Fatalf("32-bit counter wrap: rx=%v tx=%v ok=%v", rx, tx, ok)
	}
	cur.Discontinuity++
	if _, _, ok := snmpIfaceRates(prev, cur, 10); ok {
		t.Fatal("counter reset produced a traffic rate")
	}
}

func TestSNMPPortChart(t *testing.T) {
	st := testStore(t)
	const port = `WAN "2"`
	id, err := st.UpsertPVEHost("snmp-test", "dev", kindSNMP, "Ideco", time.Now().Unix(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	vm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if query := r.URL.Query().Get("query"); !strings.Contains(query, `,iface="WAN \"2\""}`) {
			t.Errorf("query does not select the requested port: %s", query)
		}
		io.WriteString(w, `{"status":"success","data":{"result":[{"metric":{},"values":[]}]}}`)
	}))
	defer vm.Close()
	server := &Server{st: st, vm: NewVM(vm.URL)}
	req := httptest.NewRequest("GET", "/chart?m=net&iface="+url.QueryEscape(port), nil)
	req.SetPathValue("id", id)
	response := httptest.NewRecorder()
	server.chart(response, req, nil)
	var chart Chart
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &chart) != nil || chart.Unit != "B/s" ||
		len(chart.Series) != 2 || chart.Series[0].Label != "Приём" || chart.Series[1].Label != "Передача" {
		t.Fatalf("chart response: %d %s", response.Code, response.Body.String())
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

func TestSNMPHardwareNames(t *testing.T) {
	st := testStore(t)
	source := "cisco-asa"
	interfaces := []any{
		map[string]any{"name": "outside", "alias": "Провайдер", "physical": true},
		map[string]any{"name": "Virtual254", "physical": false},
	}
	id, err := st.UpsertPVEHost(source, "dev", kindSNMP, "ASA", time.Now().Unix(), nil, map[string]any{"ifaces": interfaces})
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{st: st}
	for _, c := range []struct {
		body string
		code int
	}{
		{`{"outside":" GigabitEthernet0/0 "}`, http.StatusOK},
		{`{"Virtual254":"GigabitEthernet0/1"}`, http.StatusBadRequest},
	} {
		req := httptest.NewRequest("PUT", "/api/hosts/"+id+"/port-names", strings.NewReader(c.body))
		req.Header.Set("Content-Type", "application/json")
		req.SetPathValue("id", id)
		res := httptest.NewRecorder()
		server.putSNMPPortNames(res, req, &User{})
		if res.Code != c.code {
			t.Fatalf("save %s: %d %s", c.body, res.Code, res.Body)
		}
	}
	req := httptest.NewRequest("GET", "/api/hosts/"+id, nil)
	req.SetPathValue("id", id)
	res := httptest.NewRecorder()
	server.getHost(res, req, &User{})
	var view hostView
	if err := json.Unmarshal(res.Body.Bytes(), &view); err != nil || view.SNMPPortNames["outside"] != "GigabitEthernet0/0" ||
		wanPort(view.Host, "outside")["alias"] != "Провайдер" {
		t.Fatalf("port names and SNMP identity should be independent: %s (%v)", res.Body, err)
	}
	if err := st.DeleteSNMP(source); err != nil || st.Setting(snmpPortNamesPrefix+id) != "" {
		t.Fatalf("port names should be cleaned up on deletion: %v", err)
	}
}
