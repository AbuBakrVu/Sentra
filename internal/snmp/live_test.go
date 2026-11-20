package snmp

import (
	"context"
	"os"
	"testing"
)

// Проверка на настоящем агенте: SENTRA_SNMP_TEST=127.0.0.1:1161 go test ./internal/snmp
// Ожидается net-snmp с community public и пользователями sentra (SHA/AES),
// sentra256 (SHA-256/AES), sentramd5 (MD5/DES); пароль authpass123, ключ privpass123.
func TestLiveAgent(t *testing.T) {
	addr := os.Getenv("SENTRA_SNMP_TEST")
	if addr == "" {
		t.Skip("SENTRA_SNMP_TEST не задан")
	}
	ctx := context.Background()
	s, err := Poll(ctx, addr, Auth{Version: "2c", Community: "public"})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%+v", s.System)
	t.Logf("cpu=%v raw=%+v mem=%v/%v load=%v", s.CPU, s.CPURaw, s.MemUsed, s.MemTotal, s.Load1)
	for _, d := range s.Disks {
		t.Logf("disk %+v", d)
	}
	for _, f := range s.Ifaces {
		t.Logf("if %+v", f)
	}
	if s.Name == "" || len(s.Ifaces) == 0 || s.MemTotal == 0 {
		t.Error("неполные данные")
	}
	for _, u := range []struct{ user, auth, priv string }{{"sentra", "SHA", "AES"}, {"sentra256", "SHA-256", "AES"}, {"sentramd5", "MD5", "DES"}} {
		got, _, err := Probe(ctx, addr, Auth{Version: "3", User: u.user, AuthPass: "authpass123", PrivPass: "privpass123"})
		if err != nil {
			t.Errorf("%s: %v", u.user, err)
			continue
		}
		if got.AuthProto != u.auth || got.PrivProto != u.priv {
			t.Errorf("%s: подобрано %s/%s, ожидали %s/%s", u.user, got.AuthProto, got.PrivProto, u.auth, u.priv)
		}
	}
	for _, c := range []struct {
		a    Auth
		want string
	}{
		{Auth{Version: "3", User: "nobody", AuthPass: "authpass123", PrivPass: "privpass123"}, "пользователя"},
		{Auth{Version: "3", User: "sentra", AuthPass: "wrongpass1", PrivPass: "privpass123"}, ""},
		{Auth{Version: "2c", Community: "wrong"}, "не отвечает"},
	} {
		_, _, err := Probe(ctx, addr, c.a)
		t.Logf("%s/%s: %v", c.a.User, c.a.Community, err)
		if err == nil {
			t.Errorf("%+v: ждали ошибку", c.a)
		}
	}
}
