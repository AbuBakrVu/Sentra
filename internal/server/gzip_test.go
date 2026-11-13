package server

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGzipCompressesLargeJSON(t *testing.T) {
	big := `[` + strings.Repeat(`{"status":"ok","cpu_usage_percent":12},`, 400) + `{}]`
	h := withGzip(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(big[:100]))
		w.Write([]byte(big[100:]))
	}))
	req := httptest.NewRequest("GET", "/api/hosts", nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatal("большой JSON не сжат")
	}
	if rec.Body.Len()*5 > len(big) {
		t.Fatalf("сжатие слабое: %d из %d байт", rec.Body.Len(), len(big))
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(zr)
	if string(out) != big {
		t.Fatal("после распаковки ответ не совпал")
	}
}

func TestGzipSkips(t *testing.T) {
	cases := []struct {
		name, path, accept, ctype, body string
		status                          int
	}{
		{"браузер без gzip", "/api/hosts", "", "application/json", strings.Repeat("x", 5000), 200},
		{"маленький ответ", "/api/me", "gzip", "application/json", `{"login":"admin"}`, 200},
		{"двоичный файл агента", "/download/sentra-agent", "gzip", "application/octet-stream", strings.Repeat("x", 5000), 200},
		{"картинка", "/logo.png", "gzip", "image/png", strings.Repeat("x", 5000), 200},
		{"ошибка с телом", "/api/hosts", "gzip", "application/json", `{"error":"нет"}`, 401},
	}
	for _, c := range cases {
		h := withGzip(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", c.ctype)
			w.WriteHeader(c.status)
			io.WriteString(w, c.body)
		}))
		req := httptest.NewRequest("GET", c.path, nil)
		if c.accept != "" {
			req.Header.Set("Accept-Encoding", c.accept)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Header().Get("Content-Encoding") != "" || rec.Body.String() != c.body || rec.Code != c.status {
			t.Errorf("%s: сжатие %q, код %d, тело совпало %v", c.name, rec.Header().Get("Content-Encoding"), rec.Code, rec.Body.String() == c.body)
		}
	}
}

// Один упавший узел — одна проблема, а не по проблеме на каждую машину.
func TestDependentProblemsHidden(t *testing.T) {
	vm := &Problem{Kind: "down", Key: "node_offline"}
	if !dependent(vm, "pve-qemu") || !dependent(&Problem{Kind: "down", Key: "offline"}, "pve-lxc") {
		t.Fatal("недоступность машины из-за узла должна быть зависимой")
	}
	if dependent(vm, "pve-node") || dependent(&Problem{Kind: "down", Key: "offline"}, "agent") {
		t.Fatal("проблемы узла и сервера с агентом зависимыми не бывают")
	}
	if dependent(&Problem{Kind: "degraded", Key: "cpu"}, "pve-qemu") {
		t.Fatal("загрузка CPU машины — её собственная проблема")
	}
	if pluralRu(1, "машина", "машины", "машин") != "машина" || pluralRu(3, "машина", "машины", "машин") != "машины" ||
		pluralRu(12, "машина", "машины", "машин") != "машин" || pluralRu(31, "машина", "машины", "машин") != "машина" {
		t.Fatal("склонение")
	}
}
