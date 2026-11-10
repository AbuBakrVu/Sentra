package proxmox

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const resourcesJSON = `{"data":[
 {"id":"node/pve1","type":"node","node":"pve1","status":"online","cpu":0.0512,"maxcpu":16,"mem":34359738368,"maxmem":68719476736,"disk":12884901888,"maxdisk":100000000000,"uptime":864000,"level":""},
 {"id":"qemu/100","type":"qemu","node":"pve1","vmid":100,"name":"srv-1c","status":"running","template":0,"cpu":0.25,"maxcpu":4,"mem":6442450944,"maxmem":8589934592,"disk":0,"maxdisk":107374182400,"netin":1000000,"netout":2000000,"uptime":3600,"tags":"1c;prod"},
 {"id":"qemu/9000","type":"qemu","node":"pve1","vmid":9000,"name":"ubuntu-tpl","status":"stopped","template":1},
 {"id":"lxc/101","type":"lxc","node":"pve1","vmid":"101","name":"crm-01","status":"stopped","cpu":0,"maxcpu":2,"mem":0,"maxmem":2147483648,"disk":0,"maxdisk":8589934592},
 {"id":"storage/pve1/local-lvm","type":"storage","node":"pve1","storage":"local-lvm","status":"available","plugintype":"lvmthin","disk":400000000000,"maxdisk":500000000000,"shared":0}
]}`

func fake(t *testing.T, token string) *httptest.Server {
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "PVEAPIToken="+token {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/api2/json/version":
			w.Write([]byte(`{"data":{"version":"8.2.4","release":"8.2"}}`))
		case "/api2/json/cluster/resources":
			w.Write([]byte(resourcesJSON))
		case "/api2/json/nodes/pve1/status":
			w.Write([]byte(`{"data":{"loadavg":["0.52","0.40","0.31"],"memory":{"total":68719476736,"used":34359738368},"swap":{"total":0,"used":0},"rootfs":{"total":100,"used":25,"avail":75},"pveversion":"pve-manager/8.2.4","cpuinfo":{"model":"Xeon","cpus":16}}}`))
		default:
			w.WriteHeader(404)
		}
	}))
}

func TestClient(t *testing.T) {
	tok := "sentra@pve!monitor=11111111-2222-3333-4444-555555555555"
	srv := fake(t, tok)
	defer srv.Close()
	ctx := context.Background()

	fp, err := FetchFingerprint(ctx, srv.URL)
	if err != nil || len(fp) != 95 {
		t.Fatalf("fingerprint %q %v", fp, err)
	}
	c := New(srv.URL, "sentra@pve!monitor", "11111111-2222-3333-4444-555555555555", fp)
	if v, err := c.Version(ctx); err != nil || v != "8.2.4" {
		t.Fatalf("version %q %v", v, err)
	}
	res, err := c.Resources(ctx)
	if err != nil || len(res) != 5 {
		t.Fatalf("resources %d %v", len(res), err)
	}
	if res[1].VMID != 100 || res[1].CPU != 0.25 || res[3].VMID != 101 || res[2].Template != 1 {
		t.Fatalf("parse: %+v %+v", res[1], res[3])
	}
	st, err := c.NodeStatus(ctx, "pve1")
	if err != nil || len(st.LoadAvg) != 3 || st.LoadAvg[0] != 0.52 {
		t.Fatalf("status %+v %v", st, err)
	}

	// чужой сертификат
	bad := New(srv.URL, "sentra@pve!monitor", "11111111-2222-3333-4444-555555555555", strings.Repeat("AA:", 31)+"AA")
	if _, err := bad.Version(ctx); !errors.Is(err, ErrFingerprint) {
		t.Fatalf("want ErrFingerprint, got %v", err)
	}
	// неверный токен
	wrong := New(srv.URL, "sentra@pve!monitor", "00000000-0000-0000-0000-000000000000", fp)
	if _, err := wrong.Version(ctx); err == nil || !strings.Contains(err.Error(), "токен") {
		t.Fatalf("want token error, got %v", err)
	}
}

func TestNormalizeURL(t *testing.T) {
	for in, want := range map[string]string{
		"192.168.1.20":                  "https://192.168.1.20:8006",
		"https://pve.local:8006/":       "https://pve.local:8006",
		"https://192.168.1.20:8006/#v1": "https://192.168.1.20:8006",
		"pve:443":                       "https://pve:443",
	} {
		if got, err := NormalizeURL(in); err != nil || got != want {
			t.Errorf("%s: %s %v", in, got, err)
		}
	}
	if _, err := NormalizeURL("http://1.2.3.4"); err == nil {
		t.Error("http должен отклоняться")
	}
}
