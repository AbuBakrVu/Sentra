package proxmox

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUsableFiltersServiceMounts(t *testing.T) {
	in := []FS{
		{Name: "sda1", Mountpoint: "/", Type: "ext4", Total: 100, Used: 40},
		{Name: "sda1", Mountpoint: "/var/lib/docker/overlay2/x/merged", Type: "overlay", Total: 100, Used: 40},
		{Name: "sda1", Mountpoint: "/srv/bind", Type: "ext4", Total: 100, Used: 40}, // тот же раздел ещё раз
		{Name: "loop0", Mountpoint: "/snap/core22/1380", Type: "squashfs", Total: 8, Used: 8},
		{Name: "sdb1", Mountpoint: "/data", Type: "xfs", Total: 300, Used: 30},
		{Name: `\\?\Volume{1}`, Mountpoint: "System Reserved", Type: "NTFS", Total: 5, Used: 1},
		{Name: `\\?\Volume{2}`, Mountpoint: `C:\`, Type: "NTFS", Total: 500, Used: 250},
		{Name: "cdrom", Mountpoint: "/media/cd", Type: "iso9660", Total: 700, Used: 700},
		{Name: "zero", Mountpoint: "/empty", Type: "ext4", Total: 0, Used: 0},
	}
	got := Usable(in)
	want := []string{"/", "/data", "C:"}
	if len(got) != len(want) {
		t.Fatalf("получили %d разделов: %+v", len(got), got)
	}
	for i, w := range want {
		if got[i].Mountpoint != w {
			t.Errorf("раздел %d: %q, ждали %q", i, got[i].Mountpoint, w)
		}
	}
}

func TestAgentFSInfoErrors(t *testing.T) {
	code, body := 200, `{"data":{"result":[{"name":"sda1","mountpoint":"/","type":"ext4","total-bytes":1000,"used-bytes":250}]}}`
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api2/json/nodes/pve1/qemu/105/agent/get-fsinfo" {
			http.Error(w, "bad path "+r.URL.Path, 400)
			return
		}
		w.WriteHeader(code)
		w.Write([]byte(body))
	}))
	defer srv.Close()
	c := New(srv.URL, "a@pve!b", "secret", "")
	ctx := context.Background()

	fs, err := c.AgentFSInfo(ctx, "pve1", 105)
	if err != nil || len(fs) != 1 || fs[0].Used != 250 {
		t.Fatalf("ответ агента: %+v, %v", fs, err)
	}
	code, body = 500, `{"data":null,"message":"No QEMU guest agent configured\n"}`
	if _, err := c.AgentFSInfo(ctx, "pve1", 105); !errors.Is(err, ErrNoAgent) {
		t.Fatalf("ждали ErrNoAgent, получили %v", err)
	}
	code, body = 500, `{"data":null,"message":"VM 105 is not running\n"}`
	if _, err := c.AgentFSInfo(ctx, "pve1", 105); !errors.Is(err, ErrNoAgent) {
		t.Fatalf("выключенная ВМ: ждали ErrNoAgent, получили %v", err)
	}
	code, body = 403, `{"data":null}`
	if _, err := c.AgentFSInfo(ctx, "pve1", 105); !errors.Is(err, ErrAgentForbidden) {
		t.Fatalf("ждали ErrAgentForbidden, получили %v", err)
	}
}
