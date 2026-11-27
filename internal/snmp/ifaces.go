package snmp

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gosnmp/gosnmp"
)

// Net-SNMP на Linux может объявлять разъём у виртуального интерфейса.
// Учитываем известные системные имена, включая интерфейсы Ideco.
var logicalIfaceName = regexp.MustCompile(`(?i)^(?:veth|virbr|ipsec|ccwg_|fwtest$|sw_|lb_|(?:lo|lvlan|lbond|lvpn|eppp|ppp|virtual|vlan|bond|team|bridge|br|tun|tap|wg|vxlan|gre|gretap|dummy|docker|sit)(?:[0-9_.-]|$))|\.[0-9]+$`)

// PollInterface reads the counters and status for one interface without walking the device.
func PollInterface(ctx context.Context, addr string, auth Auth, index int) (Iface, uint32, error) {
	g, err := client(ctx, addr, auth, 1500*time.Millisecond, 0)
	if err != nil {
		return Iface{}, 0, err
	}
	defer g.Conn.Close()
	suffix := "." + strconv.Itoa(index)
	basic := []string{oidSysUpTime, oidIfTable + ".1" + suffix, oidIfTable + ".2" + suffix,
		oidIfTable + ".5" + suffix, oidIfTable + ".6" + suffix, oidIfTable + ".8" + suffix,
		oidIfTable + ".10" + suffix, oidIfTable + ".16" + suffix}
	keys := append(basic, oidIfXTable+".1"+suffix, oidIfXTable+".6"+suffix, oidIfXTable+".10"+suffix,
		oidIfXTable+".15"+suffix, oidIfXTable+".19"+suffix)
	m, err := get(g, keys...)
	if err != nil && ctx.Err() == nil {
		// Some older agents fail the entire request when it includes ifXTable OIDs.
		m, err = get(g, basic...)
	}
	if err != nil {
		return Iface{}, 0, err
	}
	var rows, extended []gosnmp.SnmpPDU
	for _, pdu := range m {
		switch {
		case strings.HasPrefix(pdu.Name, oidIfTable+"."):
			rows = append(rows, pdu)
		case strings.HasPrefix(pdu.Name, oidIfXTable+"."):
			extended = append(extended, pdu)
		}
	}
	ports := readIfaces(rows, extended, nil)
	if len(ports) != 1 || ports[0].Index != index {
		return Iface{}, 0, fmt.Errorf("нет данных SNMP для интерфейса %d", index)
	}
	return ports[0], uint32(num(m[oidSysUpTime])), nil
}

var physicalPortName = regexp.MustCompile(`(?i)^(?:Adaptive Security Appliance ['"]?)?((?:GigabitEthernet|FastEthernet|TenGigabitEthernet|Ethernet|Management|Gi|Fa|Te|Eth|Mgmt)[0-9]+(?:/[0-9]+)*)(?:['"]? interface)?$`)
var asaVirtualDescription = regexp.MustCompile(`(?i)^Adaptive Security Appliance ['"]?Virtual[0-9]+['"]? interface$`)

func readIfaces(ifs, xs, stack []gosnmp.SnmpPDU) []Iface {
	it, xt := table(ifs, oidIfTable), table(xs, oidIfXTable)
	upperLayers := map[string]bool{}
	for _, row := range stack {
		suffix, ok := strings.CutPrefix(row.Name, oidIfStack+".")
		higher, lower, pair := strings.Cut(suffix, ".")
		if ok && pair && lower != "0" && num(row) == 1 {
			upperLayers[higher] = true
		}
	}
	indices := map[string]bool{}
	for _, col := range []map[string]gosnmp.SnmpPDU{it[1], it[2], xt[1]} {
		for idx := range col {
			indices[idx] = true
		}
	}
	rows := make([]Iface, 0, len(indices))
	for idx := range indices {
		index, err := strconv.Atoi(idx)
		kind := int(num(it[3][idx]))
		if err != nil || index <= 0 || kind == 24 { // softwareLoopback
			continue
		}
		descr := strings.TrimSpace(str(it[2][idx]))
		name := strings.TrimSpace(str(xt[1][idx]))
		if name == "" {
			name = descr
		}
		if name == "" {
			name = fmt.Sprintf("if#%d", index)
		}
		f := Iface{
			Index: index, Name: name, Alias: strings.TrimSpace(str(xt[18][idx])),
			OperStatus: int(num(it[8][idx])), AdminStatus: int(num(it[7][idx])),
			Speed: num(it[5][idx]), Discontinuity: uint32(num(xt[19][idx])),
		}
		if mac, ok := it[6][idx].Value.([]byte); ok && len(mac) == 6 && string(mac) != "\x00\x00\x00\x00\x00\x00" {
			f.MAC = net.HardwareAddr(mac).String()
		}
		if highSpeed := num(xt[15][idx]); highSpeed > 0 {
			f.Speed = highSpeed * 1e6 // ifHighSpeed в Мбит/с
		}
		f.Physical = !upperLayers[idx] && !logicalIfaceName.MatchString(name) && !asaVirtualDescription.MatchString(descr) &&
			physicalIface(kind, int(num(xt[17][idx])), f.MAC)
		if matches := physicalPortName.FindStringSubmatch(descr); f.Physical && len(matches) > 0 && matches[1] != name {
			f.HardwareName = matches[1]
		}
		in, out := xt[6][idx], xt[10][idx]
		f.HC = in.Type == gosnmp.Counter64 && out.Type == gosnmp.Counter64
		if !f.HC {
			in, out = it[10][idx], it[16][idx]
		}
		f.Counters = f.HC || (in.Type == gosnmp.Counter32 && out.Type == gosnmp.Counter32)
		if f.Counters {
			f.In, f.Out = gosnmp.ToBigInt(in.Value).Uint64(), gosnmp.ToBigInt(out.Value).Uint64()
		}
		rows = append(rows, f)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Index < rows[j].Index })
	return rows
}

func physicalIface(kind, connector int, mac string) bool {
	switch kind {
	case 23, 53, 112, 131, 135, 136, 137, 161, 209, 258: // PPP, виртуальные интерфейсы, туннели, VLAN, LAG, bridge
		return false
	}
	if connector != 0 {
		return connector == 1
	}
	// При ограниченном SNMP-view используем тип Ethernet и ненулевой MAC.
	switch kind {
	case 6, 7, 26, 62, 69, 117:
		return mac != ""
	}
	return false
}
