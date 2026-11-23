package vmware

import (
	"context"
	"crypto/tls"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/vmware/govmomi/simulator"
)

// base и логин симулятора: vcsim принимает любой пароль для своего пользователя.
func simBase(t *testing.T, u *url.URL) (string, string, string) {
	t.Helper()
	pass, _ := u.User.Password()
	return "https://" + u.Host, u.User.Username(), pass
}

func TestNormalizeURL(t *testing.T) {
	for in, want := range map[string]string{
		"10.0.0.5":                       "https://10.0.0.5",
		"https://vcenter.local/ui/":      "https://vcenter.local",
		"vcenter.local:8443":             "https://vcenter.local:8443",
		"https://10.0.0.5:443/sdk":       "https://10.0.0.5",
		" https://esx01.corp.local/sdk ": "https://esx01.corp.local",
	} {
		got, err := NormalizeURL(in)
		if err != nil || got != want {
			t.Errorf("NormalizeURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := NormalizeURL("http://10.0.0.5"); err == nil {
		t.Error("http должен быть отклонён")
	}
}

func testModel(t *testing.T, m *simulator.Model, fn func(base, user, pass string)) {
	t.Helper()
	defer m.Remove()
	if err := m.Create(); err != nil {
		t.Fatal(err)
	}
	m.Service.TLS = new(tls.Config)
	s := m.Service.NewServer()
	defer s.Close()
	base, user, pass := simBase(t, s.URL)
	fn(base, user, pass)
}

func TestInventoryVCenter(t *testing.T) {
	m := simulator.VPX()
	m.Host = 1    // отдельный хост вне кластера
	m.Cluster = 1 // и кластер из трёх
	testModel(t, m, func(base, user, pass string) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		fp, err := FetchFingerprint(ctx, base)
		if err != nil {
			t.Fatal(err)
		}
		c := New(base, user, pass, fp)
		a, err := c.About(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !a.IsVCenter() {
			t.Errorf("ожидали vCenter, получили %q", a.APIType)
		}
		inv, err := c.Inventory(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(inv.Hosts) != 4 {
			t.Errorf("хостов %d, ожидали 4", len(inv.Hosts))
		}
		inCluster := 0
		for _, h := range inv.Hosts {
			if !h.Connected || h.MemTotal == 0 || h.CPUMhzTotal == 0 || len(h.Datastores) == 0 {
				t.Errorf("хост %s заполнен не полностью: %+v", h.Name, h)
			}
			if h.Cluster != "" {
				inCluster++
			}
		}
		if inCluster != 3 {
			t.Errorf("в кластере %d хостов, ожидали 3", inCluster)
		}
		if len(inv.VMs) == 0 || len(inv.Datastores) == 0 {
			t.Fatalf("нет машин или хранилищ: %d, %d", len(inv.VMs), len(inv.Datastores))
		}
		for _, v := range inv.VMs {
			if v.HostRef == "" || v.NumCPU == 0 || v.MemTotal == 0 {
				t.Errorf("машина %s заполнена не полностью: %+v", v.Name, v)
			}
		}
		// повторный опрос идёт в той же сессии
		if _, err := c.Inventory(ctx); err != nil {
			t.Fatal(err)
		}
		c.Logout(ctx)
	})
}

func TestInventoryESXi(t *testing.T) {
	testModel(t, simulator.ESX(), func(base, user, pass string) {
		ctx := context.Background()
		fp, _ := FetchFingerprint(ctx, base)
		c := New(base, user, pass, fp)
		a, err := c.About(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if a.IsVCenter() {
			t.Error("ESXi принят за vCenter")
		}
		inv, err := c.Inventory(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(inv.Hosts) != 1 || len(inv.VMs) == 0 {
			t.Errorf("хостов %d, машин %d", len(inv.Hosts), len(inv.VMs))
		}
	})
}

func TestWrongFingerprintAndLogin(t *testing.T) {
	m := simulator.ESX()
	defer m.Remove()
	if err := m.Create(); err != nil {
		t.Fatal(err)
	}
	m.Service.Listen = &url.URL{User: url.UserPassword("root", "secret")}
	m.Service.TLS = new(tls.Config)
	s := m.Service.NewServer()
	defer s.Close()
	ctx := context.Background()
	base := "https://" + s.URL.Host
	fp, err := FetchFingerprint(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(base, "root", "secret", fp).About(ctx); err != nil {
		t.Fatalf("верный пароль: %v", err)
	}
	if _, err := New(base, "root", "wrong", fp).About(ctx); !errors.Is(err, ErrLogin) {
		t.Errorf("неверный пароль: %v", err)
	}
	bad := strings.Repeat("00:", 31) + "00"
	if _, err := New(base, "root", "secret", bad).About(ctx); !errors.Is(err, ErrFingerprint) {
		t.Errorf("чужой отпечаток: %v", err)
	}
}

func TestUsableDisks(t *testing.T) {
	got := UsableDisks([]Disk{
		{Path: "/", Capacity: 50 << 30, Free: 10 << 30},
		{Path: "/boot", Capacity: 1 << 30},
		{Path: `C:\`, Capacity: 100 << 30},
		{Path: `D:\Data\`, Capacity: 100 << 30},
		{Path: "System Reserved", Capacity: 500 << 20},
		{Path: "/", Capacity: 50 << 30},
		{Path: "/tiny", Capacity: 1 << 20},
	})
	want := []string{"/", "C:", `D:\Data`}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i, d := range got {
		if d.Path != want[i] {
			t.Errorf("%d: %q, want %q", i, d.Path, want[i])
		}
	}
}
