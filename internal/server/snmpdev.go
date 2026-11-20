package server

import (
	"context"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"sentra/internal/proto"
	"sentra/internal/snmp"
)

// Опрос SNMP-устройств (межсетевые экраны, маршрутизаторы, коммутаторы)
// раз в 30 секунд. Скорость интерфейсов и загрузку CPU по сырым счётчикам
// считаем по разнице с прошлым опросом, поэтому держим его в памяти.

const kindSNMP = "snmp"

type snmpPrev struct {
	at  time.Time
	cpu *snmp.CPURaw
	ifs map[int][2]uint64 // индекс → байты приём, передача
}

type snmpPoller struct {
	mu   sync.Mutex
	prev map[string]*snmpPrev
	kick chan string
}

func newSNMPPoller() *snmpPoller {
	return &snmpPoller{prev: map[string]*snmpPrev{}, kick: make(chan string, 8)}
}

func (s *Server) startSNMP(ctx context.Context) {
	go func() {
		t := time.NewTicker(pvePollEvery)
		defer t.Stop()
		s.pollAllSNMP(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.pollAllSNMP(ctx)
			case id := <-s.snmp.kick:
				if d, err := s.st.SNMPDevice(id); err == nil {
					s.pollSNMP(ctx, d)
				}
			}
		}
	}()
}

func (s *Server) pollAllSNMP(ctx context.Context) {
	devs, err := s.st.SNMPDevices()
	if err != nil {
		log.Printf("snmp: список устройств: %v", err)
		return
	}
	sem := make(chan struct{}, 16) // десятки коммутаторов не опрашиваем все разом
	var wg sync.WaitGroup
	for _, d := range devs {
		wg.Add(1)
		sem <- struct{}{}
		go func(d *SNMPDevice) {
			defer func() { <-sem; wg.Done() }()
			s.pollSNMP(ctx, d)
		}(d)
	}
	wg.Wait()
}

func (s *Server) pollSNMP(ctx context.Context, d *SNMPDevice) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	if err := s.collectSNMP(ctx, d); err != nil {
		if err.Error() != d.LastError {
			log.Printf("snmp %s (%s): %v", d.Name, d.Addr, err)
		}
		s.st.SNMPResult(d.ID, false, err.Error())
		// last_seen не обновляем: через 3 минуты молчания откроется «Нет ответа по SNMP»
	}
}

// vendorByOID — производитель по sysObjectID (номер организации в IANA).
var vendorByOID = map[string]string{
	"9": "Cisco", "2011": "Huawei", "14988": "MikroTik", "4526": "Netgear", "11863": "TP-Link",
	"171": "D-Link", "25506": "HPE", "11": "HP", "2636": "Juniper", "12356": "Fortinet",
	"25461": "Palo Alto", "6876": "VMware", "6574": "Synology", "24681": "QNAP", "41112": "Ubiquiti",
	"8072": "Linux", "311": "Microsoft", "674": "Dell", "232": "HPE", "1916": "Extreme",
	"30065": "Arista", "27282": "Eltex", "35265": "Eltex", "45956": "UserGate", "3375": "F5",
}

// productOf — продукт по описанию системы: межсетевые экраны на Linux
// (Ideco, UserGate) отдают sysObjectID от net-snmp, и по нему видно только «Linux».
func productOf(descr, objectID string) string {
	d := strings.ToLower(descr)
	for _, p := range []struct{ key, name string }{
		{"ideco", "Ideco NGFW"}, {"usergate", "UserGate"}, {"routeros", "MikroTik"}, {"pfsense", "pfSense"},
		{"opnsense", "OPNsense"}, {"synology", "Synology"}, {"proxmox", "Proxmox"}, {"kerio", "Kerio Control"},
		{"continent", "Континент"}, {"vipnet", "ViPNet"}, {"eltex", "Eltex"}, {"keenetic", "Keenetic"},
	} {
		if strings.Contains(d, p.key) {
			return p.name
		}
	}
	return vendorOf(objectID)
}

func vendorOf(objectID string) string {
	p := strings.TrimPrefix(objectID, ".")
	if !strings.HasPrefix(p, "1.3.6.1.4.1.") {
		return ""
	}
	num, _, _ := strings.Cut(strings.TrimPrefix(p, "1.3.6.1.4.1."), ".")
	return vendorByOID[num]
}

func (s *Server) collectSNMP(ctx context.Context, d *SNMPDevice) error {
	snap, err := snmp.Poll(ctx, d.Addr, d.Auth)
	if err != nil {
		return err
	}
	now := time.Now()

	s.snmp.mu.Lock()
	prev := s.snmp.prev[d.ID]
	cur := &snmpPrev{at: now, cpu: snap.CPURaw, ifs: map[int][2]uint64{}}
	s.snmp.prev[d.ID] = cur
	s.snmp.mu.Unlock()
	dt := 0.0
	if prev != nil {
		dt = now.Sub(prev.at).Seconds()
	}

	var samples []proto.Sample
	add := func(n string, v float64, kv ...string) {
		var l map[string]string
		if len(kv) == 2 {
			l = map[string]string{kv[0]: kv[1]}
		}
		samples = append(samples, proto.Sample{Name: n, Labels: l, Value: v})
	}
	switch {
	case snap.CPU >= 0:
		add(proto.MCPUUsage, round2(min(100, snap.CPU)))
	case snap.CPURaw != nil && prev != nil && prev.cpu != nil && snap.CPURaw.Total > prev.cpu.Total:
		add(proto.MCPUUsage, pct(snap.CPURaw.Busy-prev.cpu.Busy, snap.CPURaw.Total-prev.cpu.Total))
	}
	if snap.Uptime > 0 {
		add(proto.MUptime, snap.Uptime)
	}
	if snap.Load1 >= 0 {
		add(proto.MLoad1, snap.Load1)
	}
	if snap.MemTotal > 0 {
		add(proto.MMemTotal, snap.MemTotal)
		add(proto.MMemUsed, snap.MemUsed)
		add(proto.MMemUsedPct, pct(snap.MemUsed, snap.MemTotal))
	}
	for _, dk := range snap.Disks {
		add(proto.MFsUsedPct, pct(dk.Used, dk.Total), "mount", dk.Mount)
		add(proto.MFsFree, dk.Total-dk.Used, "mount", dk.Mount)
		add(proto.MFsTotal, dk.Total, "mount", dk.Mount)
	}

	// интерфейсы, которые хоть раз были подняты: за ними следим (см. problems.go)
	old, _ := s.st.SNMPHostMeta(d.ID)
	watched := map[string]bool{}
	if w, ok := old["if_watch"].([]any); ok {
		for _, x := range w {
			if n, ok := x.(string); ok {
				watched[n] = true
			}
		}
	}
	var ifaces []map[string]any
	for _, f := range snap.Ifaces {
		cur.ifs[f.Index] = [2]uint64{f.In, f.Out}
		item := map[string]any{"name": f.Name, "alias": f.Alias, "up": f.Up, "admin_up": f.AdminUp, "speed": f.Speed}
		if f.Up && f.AdminUp {
			watched[f.Name] = true
		}
		if !f.AdminUp {
			delete(watched, f.Name) // порт выключили намеренно — больше не следим
		}
		if prev != nil && dt > 0 {
			if p, ok := prev.ifs[f.Index]; ok && f.In >= p[0] && f.Out >= p[1] {
				rx, tx := float64(f.In-p[0])/dt, float64(f.Out-p[1])/dt
				// 32-битные счётчики на гигабите переполняются за ~34 с: такие скачки отбрасываем
				if f.HC || (rx < 1.25e8 && tx < 1.25e8) {
					item["rx"], item["tx"] = round2(rx), round2(tx)
					if f.Up {
						add(proto.MNetRx, round2(rx), "iface", f.Name)
						add(proto.MNetTx, round2(tx), "iface", f.Name)
					}
				}
			}
		}
		ifaces = append(ifaces, item)
	}
	wl := make([]string, 0, len(watched))
	for n := range watched {
		wl = append(wl, n)
	}
	sort.Strings(wl)

	name := d.Name
	if name == "" {
		name = snap.Name
	}
	meta := map[string]any{
		"pve_name": name, "addr": d.Addr, "snmp_version": d.Auth.Version,
		"sys_name": snap.Name, "sys_descr": snap.Descr, "location": snap.Location, "contact": snap.Contact,
		"object_id": snap.ObjectID, "vendor": productOf(snap.Descr, snap.ObjectID), "uptime": snap.Uptime,
		"ifaces": ifaces, "if_watch": wl,
	}
	if snap.MemTotal > 0 {
		meta["maxmem"] = snap.MemTotal
	}
	id, err := s.st.UpsertPVEHost(d.ID, "dev", kindSNMP, name, now.Unix(), nil, meta)
	if err != nil {
		return err
	}
	if len(samples) > 0 {
		if err := s.vm.Import(ctx, id, now.UnixMilli(), samples); err != nil {
			return err
		}
	}
	s.st.UpsertPVEHost(d.ID, "dev", kindSNMP, name, now.Unix(), summarize(samples), meta)
	if h, err := s.st.Host(id); err == nil {
		s.eng.Eval(h, now)
	}
	s.st.SNMPResult(d.ID, true, "")
	return nil
}

// ---------- API ----------

type snmpIn struct {
	Name      string `json:"name"`
	Addr      string `json:"addr"`
	Version   string `json:"version"`
	Community string `json:"community"`
	User      string `json:"user"`
	AuthProto string `json:"auth_proto"`
	AuthPass  string `json:"auth_pass"`
	PrivProto string `json:"priv_proto"`
	PrivPass  string `json:"priv_pass"`
}

func (in snmpIn) auth() snmp.Auth {
	v := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(in.Version)), "v")
	if v == "2" {
		v = "2c"
	}
	return snmp.Auth{Version: v, Community: in.Community, User: strings.TrimSpace(in.User),
		AuthProto: strings.ToUpper(strings.TrimSpace(in.AuthProto)), AuthPass: in.AuthPass,
		PrivProto: strings.ToUpper(strings.TrimSpace(in.PrivProto)), PrivPass: in.PrivPass}
}

func (s *Server) listSNMP(w http.ResponseWriter, r *http.Request, _ *User) {
	ds, err := s.st.SNMPDevices()
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, ds)
}

func (s *Server) addSNMP(w http.ResponseWriter, r *http.Request, _ *User) {
	var in snmpIn
	if err := readJSON(r, &in, 8192); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	addr, err := snmp.NormalizeAddr(in.Addr)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	a := in.auth()
	if err := a.Validate(); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	name := strings.TrimSpace(in.Name)
	if len([]rune(name)) > 100 {
		httpErr(w, 400, "название до 100 символов")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 50*time.Second)
	defer cancel()
	a, sys, err := snmp.Probe(ctx, addr, a)
	if err != nil {
		httpErr(w, 502, err.Error())
		return
	}
	if name == "" {
		name = sys.Name
	}
	if name == "" {
		name = addr
	}
	id, err := s.st.AddSNMP(name, addr, a)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	// запись появляется на обзоре сразу, данные — после первого опроса
	s.st.UpsertPVEHost(id, "dev", kindSNMP, name, time.Now().Unix(), nil, map[string]any{
		"pve_name": name, "addr": addr, "snmp_version": a.Version, "sys_name": sys.Name, "sys_descr": sys.Descr,
		"object_id": sys.ObjectID, "vendor": productOf(sys.Descr, sys.ObjectID)})
	select {
	case s.snmp.kick <- id:
	default:
	}
	log.Printf("добавлено SNMP-устройство %s (%s), v%s %s/%s", name, addr, a.Version, a.AuthProto, a.PrivProto)
	writeJSON(w, 200, map[string]any{"id": id, "name": name, "descr": sys.Descr, "version": a.Version,
		"auth_proto": a.AuthProto, "priv_proto": a.PrivProto})
}

// updateSNMP меняет адрес или доступ: новые параметры сначала проверяются.
func (s *Server) updateSNMP(w http.ResponseWriter, r *http.Request, _ *User) {
	d, err := s.st.SNMPDevice(r.PathValue("id"))
	if err != nil {
		httpErr(w, 404, "устройство не найдено")
		return
	}
	var in snmpIn
	if err := readJSON(r, &in, 8192); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	addr := d.Addr
	if strings.TrimSpace(in.Addr) != "" {
		if addr, err = snmp.NormalizeAddr(in.Addr); err != nil {
			httpErr(w, 400, err.Error())
			return
		}
	}
	a := in.auth()
	if err := a.Validate(); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 50*time.Second)
	defer cancel()
	if a, _, err = snmp.Probe(ctx, addr, a); err != nil {
		httpErr(w, 502, err.Error())
		return
	}
	s.st.UpdateSNMPAuth(d.ID, addr, a)
	select {
	case s.snmp.kick <- d.ID:
	default:
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) deleteSNMP(w http.ResponseWriter, r *http.Request, _ *User) {
	id := r.PathValue("id")
	if err := s.st.DeleteSNMP(id); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	s.snmp.mu.Lock()
	delete(s.snmp.prev, id)
	s.snmp.mu.Unlock()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// snmpIfaceProblems: интерфейс, который работал, упал при включённом порте —
// предупреждение. Порты, которые никогда не поднимались (пустые разъёмы
// коммутатора), и выключенные администратором не трогаем.
func snmpIfaceProblems(h *Host, want map[string]desired) {
	watched := map[string]bool{}
	if w, ok := h.Meta["if_watch"].([]any); ok {
		for _, x := range w {
			if n, ok := x.(string); ok {
				watched[n] = true
			}
		}
	}
	ifs, _ := h.Meta["ifaces"].([]any)
	for _, raw := range ifs {
		f, _ := raw.(map[string]any)
		name, _ := f["name"].(string)
		up, _ := f["up"].(bool)
		adminUp, _ := f["admin_up"].(bool)
		if name == "" || up || !adminUp || !watched[name] {
			continue
		}
		t := "Интерфейс " + name + " не на связи"
		if al, _ := f["alias"].(string); al != "" && al != name {
			t = "Интерфейс " + name + " (" + al + ") не на связи"
		}
		want["if:"+name] = desired{"degraded", 1, t, 0}
	}
}
