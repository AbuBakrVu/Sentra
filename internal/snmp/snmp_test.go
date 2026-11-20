package snmp

import "testing"

func TestNormalizeAddr(t *testing.T) {
	for in, want := range map[string]string{
		"192.168.1.1":     "192.168.1.1:161",
		" 10.0.0.1:1161 ": "10.0.0.1:1161",
		"udp://10.0.0.1":  "10.0.0.1:161",
		"ngfw.local":      "ngfw.local:161",
		"[fe80::1]:161":   "[fe80::1]:161",
	} {
		got, err := NormalizeAddr(in)
		if err != nil || got != want {
			t.Errorf("%q → %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "10.0.0.1:0", "10.0.0.1:99999", "10.0.0.0/24"} {
		if _, err := NormalizeAddr(bad); err == nil {
			t.Errorf("%q: ждали ошибку", bad)
		}
	}
}

func TestValidate(t *testing.T) {
	ok := []Auth{
		{Version: "2c", Community: "public"},
		{Version: "3", User: "sentra", AuthPass: "12345678", PrivPass: "12345678"},
		{Version: "3", User: "sentra", AuthPass: "12345678"},
		{Version: "3", User: "sentra", AuthPass: "12345678", AuthProto: "SHA-256", PrivPass: "12345678", PrivProto: "AES"},
	}
	for _, a := range ok {
		if err := a.Validate(); err != nil {
			t.Errorf("%+v: %v", a, err)
		}
	}
	bad := []Auth{
		{Version: "2c"},
		{Version: "1", Community: "public"},
		{Version: "3"},
		{Version: "3", User: "u", AuthPass: "short"},
		{Version: "3", User: "u", PrivPass: "12345678"},
		{Version: "3", User: "u", AuthPass: "12345678", AuthProto: "SHA1"},
	}
	for _, a := range bad {
		if err := a.Validate(); err == nil {
			t.Errorf("%+v: ждали ошибку", a)
		}
	}
}

func TestUsableDisks(t *testing.T) {
	got := usableDisks([]Disk{
		{Mount: "/", Total: 50 << 30},
		{Mount: "/boot", Total: 1 << 30},
		{Mount: "/run", Total: 1 << 30},
		{Mount: "/dev/shm", Total: 4 << 30},
		{Mount: `C:\ Label:  Serial Number 12ab`, Total: 100 << 30},
		{Mount: "/var/log", Total: 20 << 30},
		{Mount: "/tiny", Total: 1 << 20},
	})
	want := []string{"/", "/var/log", "C:"}
	if len(got) != len(want) {
		t.Fatalf("%+v", got)
	}
	for i := range want {
		if got[i].Mount != want[i] {
			t.Errorf("%d: %q, want %q", i, got[i].Mount, want[i])
		}
	}
}
