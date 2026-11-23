package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sentra/internal/proxmox"
)

func TestPBSConnectionAndBackupFreshness(t *testing.T) {
	const token = "sentra@pbs!monitor"
	const secret = "test-pbs-secret"
	now := time.Now()
	var fresh atomic.Bool
	pbs := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "PBSAPIToken "+token+":"+secret {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api2/json/admin/datastore/backup/status":
			writeJSON(w, 200, map[string]any{"data": map[string]int{"total": 1000000, "used": 300000, "avail": 700000}})
		case "/api2/json/admin/datastore/backup/snapshots":
			if r.URL.Query().Get("ns") != "site/servers" {
				t.Error("wrong namespace")
			}
			last := now.Add(-48 * time.Hour).Unix()
			if fresh.Load() {
				last = now.Unix()
			}
			writeJSON(w, 200, map[string]any{"data": []map[string]any{
				{"backup-type": "vm", "backup-id": "100", "backup-time": last, "size": 1024},
				{"backup-type": "vm", "backup-id": "100", "backup-time": now.Unix()},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer pbs.Close()
	fvm := &fakeVM{}
	vm := httptest.NewServer(fvm)
	defer vm.Close()
	st := testStore(t)
	s := &Server{st: st, vm: NewVM(vm.URL), eng: NewEngine(st)}
	add := func(pin string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"url": pbs.URL, "datastore": "backup", "namespace": "site/servers",
			"token_id": token, "secret": secret, "fingerprint": pin})
		rec := httptest.NewRecorder()
		s.addPBS(rec, httptest.NewRequest(http.MethodPost, "/api/pbs", bytes.NewReader(body)), &User{Role: "admin"})
		return rec
	}
	if r := add(strings.Repeat("0", 64)); r.Code != 502 || !strings.Contains(r.Body.String(), "отпечаток") {
		t.Fatalf("wrong fingerprint: %d %s", r.Code, r.Body)
	}
	r := add(proxmox.Fingerprint(pbs.Certificate()))
	if r.Code != 201 {
		t.Fatalf("add PBS: %d %s", r.Code, r.Body)
	}
	var added pbsSource
	if err := json.Unmarshal(r.Body.Bytes(), &added); err != nil || added.ID == "" || added.WarnHours != 36 || strings.Contains(r.Body.String(), secret) {
		t.Fatalf("unexpected connection response: %s", r.Body)
	}
	if open, err := st.OpenProblemsForHost(added.ID); err != nil || len(open) != 1 || open[0].Key != "backup_stale" {
		t.Fatalf("unfinished snapshot hid an old backup: %v %v", open, err)
	}
	if !fvm.has("sentra_fs_used_percent") {
		t.Error("datastore fill metric was not imported")
	}
	public := httptest.NewRecorder()
	s.listPBS(public, httptest.NewRequest(http.MethodGet, "/api/pbs", nil), &User{Role: "admin"})
	if strings.Contains(public.Body.String(), secret) || !strings.Contains(public.Body.String(), added.ID) {
		t.Fatal("invalid public connection list")
	}
	sources, err := st.pbsSources()
	if err != nil || len(sources) != 1 {
		t.Fatalf("expected one saved connection: %v %v", len(sources), err)
	}
	fresh.Store(true)
	s.pollPBS(context.Background(), sources[0])
	if open, err := st.OpenProblemsForHost(added.ID); err != nil || len(open) != 0 {
		t.Fatalf("fresh completed copy did not resolve the problem: %v %v", open, err)
	}
	if err := st.DeleteHost(added.ID); err != nil {
		t.Fatal(err)
	}
	if sources, err := st.pbsSources(); err != nil || len(sources) != 0 {
		t.Fatal("PBS connection survived deletion")
	}
}
