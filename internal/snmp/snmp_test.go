package snmp

import (
	"fmt"
	"testing"

	"github.com/gosnmp/gosnmp"
)

func TestReadIfaces(t *testing.T) {
	var base, extended []gosnmp.SnmpPDU
	for i, name := range []string{"Eeth2", "eth6", "eth7_b1", "Lvlan42", "eth6.42", "Lbond23", "veth_vpn_host", "Leth6", "lo"} {
		index := i + 1
		kind := 6
		if name == "lo" {
			kind = 24
		}
		base = append(base,
			gosnmp.SnmpPDU{Name: fmt.Sprintf("%s.1.%d", oidIfTable, index), Type: gosnmp.Integer, Value: index},
			gosnmp.SnmpPDU{Name: fmt.Sprintf("%s.3.%d", oidIfTable, index), Type: gosnmp.Integer, Value: kind},
			gosnmp.SnmpPDU{Name: fmt.Sprintf("%s.6.%d", oidIfTable, index), Type: gosnmp.OctetString, Value: []byte{2, 1, 2, 3, 4, byte(index)}},
			gosnmp.SnmpPDU{Name: fmt.Sprintf("%s.7.%d", oidIfTable, index), Type: gosnmp.Integer, Value: 1},
			gosnmp.SnmpPDU{Name: fmt.Sprintf("%s.8.%d", oidIfTable, index), Type: gosnmp.Integer, Value: 2},
		)
		extended = append(extended, gosnmp.SnmpPDU{Name: fmt.Sprintf("%s.1.%d", oidIfXTable, index), Type: gosnmp.OctetString, Value: name})
		if index != 3 { // разъём eth7_b1 определяется по Ethernet и MAC без ifConnectorPresent
			extended = append(extended, gosnmp.SnmpPDU{Name: fmt.Sprintf("%s.17.%d", oidIfXTable, index), Type: gosnmp.Integer, Value: 1})
		}
	}
	extended = append(extended,
		gosnmp.SnmpPDU{Name: oidIfXTable + ".6.1", Type: gosnmp.Counter64, Value: uint64(1<<54 + 1)},
		gosnmp.SnmpPDU{Name: oidIfXTable + ".10.1", Type: gosnmp.Counter64, Value: uint64(1<<54 + 2)},
	)
	stack := []gosnmp.SnmpPDU{{Name: oidIfStack + ".8.2", Type: gosnmp.Integer, Value: 1}}
	rows := readIfaces(base, extended, stack)
	if len(rows) != 8 {
		t.Fatalf("got %d interfaces, want 8", len(rows))
	}
	for i, row := range rows {
		if row.Index != i+1 || row.Physical != (i < 3) {
			t.Errorf("interface %d: %+v", i+1, row)
		}
	}
	if got := rows[0]; got.MAC != "02:01:02:03:04:01" || got.OperStatus != 2 || !got.Counters || !got.HC || got.In != 1<<54+1 || got.Out != 1<<54+2 {
		t.Errorf("physical port metadata: %+v", got)
	}
	if rows[1].Counters {
		t.Error("missing counters must not become zero traffic")
	}
}

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
