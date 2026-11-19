package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"sentra/internal/proto"
	"sentra/internal/vmware"
)

// Опрос VMware (vCenter или отдельный ESXi) раз в 30 секунд, как и Proxmox.
// Хосты ESXi становятся устройствами vmw-host, машины — vmw-vm.

type vmwPoller struct {
	mu      sync.Mutex
	clients map[string]*vmwConn // id подключения → клиент с открытой сессией
	kick    chan string

	wmu    sync.Mutex
	warned map[string]bool
}

type vmwConn struct {
	key string // адрес, логин, пароль и отпечаток: сменились — нужен новый клиент
	cl  *vmware.Client
}

func newVMWPoller() *vmwPoller {
	return &vmwPoller{clients: map[string]*vmwConn{}, kick: make(chan string, 8), warned: map[string]bool{}}
}

// client возвращает клиент подключения, сохраняя сессию между опросами.
func (p *vmwPoller) client(src *VMWSource) *vmware.Client {
	key := src.URL + "\x00" + src.Username + "\x00" + src.Password + "\x00" + src.Fingerprint
	p.mu.Lock()
	defer p.mu.Unlock()
	if c := p.clients[src.ID]; c != nil && c.key == key {
		return c.cl
	} else if c != nil {
		go c.cl.Logout(context.Background())
	}
	cl := vmware.New(src.URL, src.Username, src.Password, src.Fingerprint)
	p.clients[src.ID] = &vmwConn{key: key, cl: cl}
	return cl
}

// drop закрывает сессию подключения: его удалили или сменили сертификат.
func (p *vmwPoller) drop(id string) {
	p.mu.Lock()
	c := p.clients[id]
	delete(p.clients, id)
	p.mu.Unlock()
	if c != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c.cl.Logout(ctx)
	}
}

func (p *vmwPoller) warnOnce(src string) bool {
	p.wmu.Lock()
	defer p.wmu.Unlock()
	if p.warned[src] {
		return false
	}
	p.warned[src] = true
	return true
}

func (p *vmwPoller) clearWarn(src string) {
	p.wmu.Lock()
	delete(p.warned, src)
	p.wmu.Unlock()
}

func (s *Server) startVMW(ctx context.Context) {
	go func() {
		t := time.NewTicker(pvePollEvery)
		defer t.Stop()
		s.pollAllVMW(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.pollAllVMW(ctx)
			case id := <-s.vmw.kick:
				if src, err := s.st.VMWSource(id); err == nil {
					s.pollVMW(ctx, src)
				}
			}
		}
	}()
}

func (s *Server) pollAllVMW(ctx context.Context) {
	srcs, err := s.st.VMWSources()
	if err != nil {
		log.Printf("vmware: список подключений: %v", err)
		return
	}
	var wg sync.WaitGroup
	for _, src := range srcs {
		wg.Add(1)
		go func(src *VMWSource) { defer wg.Done(); s.pollVMW(ctx, src) }(src)
	}
	wg.Wait()
}

func (s *Server) pollVMW(ctx context.Context, src *VMWSource) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	started := time.Now()
	err := s.collectVMW(ctx, src)
	if d := time.Since(started); d > pveSlowPoll || debugPVE() {
		log.Printf("vmware %s: опрос занял %.1f с", src.Name, d.Seconds())
	}
	if err != nil {
		if err.Error() != src.LastError {
			log.Printf("vmware %s (%s): %v", src.Name, src.URL, err)
		}
		s.st.VMWResult(src.ID, false, err.Error(), nil)
	}
}

// Состояние VMware Tools в машине, оно же подсказка в интерфейсе (meta.tools).
const (
	toolsOK           = "ok"
	toolsNotRunning   = "notrunning"
	toolsNotInstalled = "notinstalled"
)

func toolsState(v *vmware.VM) string {
	switch {
	case v.ToolsVersion == "guestToolsNotInstalled":
		return toolsNotInstalled
	case v.Tools != "guestToolsRunning" && v.Tools != "guestToolsExecutingScripts":
		return toolsNotRunning
	}
	return toolsOK
}

func vmwStatus(power string) string {
	switch power {
	case "poweredOn":
		return "running"
	case "suspended":
		return "suspended"
	}
	return "stopped"
}

func pct(used, total float64) float64 {
	if total <= 0 {
		return 0
	}
	v := 100 * used / total
	if v > 100 {
		v = 100
	}
	return round2(v)
}

func (s *Server) collectVMW(ctx context.Context, src *VMWSource) error {
	cl := s.vmw.client(src)
	inv, err := cl.Inventory(ctx)
	if err != nil {
		return err
	}
	if len(inv.Hosts) == 0 {
		return errors.New("VMware не показал ни одного хоста: у пользователя, видимо, нет прав на объекты. " +
			"Выдайте ему роль Read-only на корне vCenter с галкой «Propagate to children»")
	}
	now := time.Now()
	ts := now.UnixMilli()
	kindName := "esxi"
	if src.APIType == "VirtualCenter" {
		kindName = "vcenter"
	}

	hosts := map[string]*vmware.Host{}
	sort.Slice(inv.Hosts, func(i, j int) bool { return inv.Hosts[i].Name < inv.Hosts[j].Name })
	// Общее хранилище (SAN, NFS) видят все хосты кластера. Метрики и проблемы
	// заводим у одного — первого по имени из подключённых, — иначе одно
	// заполненное хранилище даст столько проблем, сколько хостов в кластере.
	dsOwner := map[string]string{}
	for i := range inv.Hosts {
		h := &inv.Hosts[i]
		hosts[h.Ref] = h
		if !h.Connected {
			continue
		}
		for _, d := range h.Datastores {
			if _, ok := dsOwner[d]; !ok {
				dsOwner[d] = h.Ref
			}
		}
	}

	seen := map[string]bool{}
	sum := map[string]any{"hosts": 0, "vms": 0, "running": 0}
	inc := func(k string) { sum[k] = sum[k].(int) + 1 }

	save := func(extID, kind, name string, meta map[string]any, samples []proto.Sample) error {
		seen[extID] = true
		meta["pve_name"] = name // имя из источника: пока пользователь его не менял, обновляем
		id, err := s.st.UpsertPVEHost(src.ID, extID, kind, name, now.Unix(), nil, meta)
		if err != nil {
			return fmt.Errorf("сохранение %s: %w", extID, err)
		}
		if len(samples) > 0 {
			if err := s.vm.Import(ctx, id, ts, samples); err != nil {
				return fmt.Errorf("хранилище метрик: %w", err)
			}
		}
		s.st.UpsertPVEHost(src.ID, extID, kind, name, now.Unix(), summarize(samples), meta)
		if h, err := s.st.Host(id); err == nil {
			s.eng.Eval(h, now)
		}
		return nil
	}

	for _, h := range inv.Hosts {
		inc("hosts")
		status := "offline"
		if h.Connected {
			status = "online"
		}
		meta := map[string]any{
			"source_name": src.Name, "source_kind": kindName, "node": h.Name, "type": "host", "status": status,
			"connection": h.Connection, "power": h.Power, "maintenance": h.Maintenance,
			"maxcpu": float64(h.Threads), "cores": float64(h.Cores), "maxmem": h.MemTotal, "uptime": h.Uptime,
			"esx_version": h.Version, "product": h.Product, "cpu_model": h.CPUModel,
			"vendor": strings.TrimSpace(h.Vendor + " " + h.Model), "cluster": h.Cluster,
		}
		var samples []proto.Sample
		add := func(n string, v float64, kv ...string) {
			var l map[string]string
			if len(kv) == 2 {
				l = map[string]string{kv[0]: kv[1]}
			}
			samples = append(samples, proto.Sample{Name: n, Labels: l, Value: v})
		}
		var stList []map[string]any
		for _, ref := range h.Datastores {
			d, ok := inv.Datastores[ref]
			if !ok || d.Capacity <= 0 {
				continue
			}
			p := pct(d.Capacity-d.Free, d.Capacity)
			stList = append(stList, map[string]any{"name": d.Name, "type": d.Type, "used": d.Capacity - d.Free,
				"total": d.Capacity, "percent": p, "shared": d.Shared, "accessible": d.Accessible})
			if h.Connected && d.Accessible && dsOwner[ref] == h.Ref {
				add(proto.MFsUsedPct, p, "mount", d.Name)
				add(proto.MFsFree, d.Free, "mount", d.Name)
				add(proto.MFsTotal, d.Capacity, "mount", d.Name)
			}
		}
		sort.Slice(stList, func(i, j int) bool { return stList[i]["name"].(string) < stList[j]["name"].(string) })
		meta["storages"] = stList
		if h.Connected {
			add(proto.MCPUUsage, pct(h.CPUMhzUsed, h.CPUMhzTotal))
			add(proto.MUptime, h.Uptime)
			if h.MemTotal > 0 {
				add(proto.MMemTotal, h.MemTotal)
				add(proto.MMemUsed, h.MemUsed)
				add(proto.MMemUsedPct, pct(h.MemUsed, h.MemTotal))
			}
		}
		if err := save("host/"+h.Ref, kindVMWHost, h.Name, meta, samples); err != nil {
			return err
		}
	}

	for i := range inv.VMs {
		v := &inv.VMs[i]
		if v.Template {
			continue // шаблоны не машины
		}
		inc("vms")
		status := vmwStatus(v.Power)
		h := hosts[v.HostRef]
		meta := map[string]any{
			"source_name": src.Name, "source_kind": kindName, "type": "vm", "status": status,
			"vmid": v.Ref, "maxcpu": float64(v.NumCPU), "maxmem": v.MemTotal, "maxdisk": v.StorageUsed,
			"uptime": v.Uptime, "guest_os": v.GuestOS, "ip": v.IP, "guest_hostname": v.GuestHost,
			"connection": v.Connection,
		}
		if h != nil {
			meta["node"] = h.Name
			meta["cluster"] = h.Cluster
		}
		nodeDown := h == nil || !h.Connected || (v.Connection != "" && v.Connection != "connected")
		if nodeDown {
			meta["node_offline"] = true
		}
		var samples []proto.Sample
		add := func(n string, val float64, kv ...string) {
			var l map[string]string
			if len(kv) == 2 {
				l = map[string]string{kv[0]: kv[1]}
			}
			samples = append(samples, proto.Sample{Name: n, Labels: l, Value: val})
		}
		if status == "running" && !nodeDown {
			inc("running")
			add(proto.MCPUUsage, pct(v.CPUMhzUsed, float64(v.NumCPU)*h.CPUMhzPerCore))
			add(proto.MUptime, v.Uptime)
			if v.MemTotal > 0 {
				// активная память по оценке ESXi: столько же показывает vSphere как Guest memory
				add(proto.MMemTotal, v.MemTotal)
				add(proto.MMemUsed, v.MemActive)
				add(proto.MMemUsedPct, pct(v.MemActive, v.MemTotal))
			}
			tools := toolsState(v)
			meta["tools"] = tools
			if tools == toolsOK {
				for _, d := range vmware.UsableDisks(v.Disks) {
					add(proto.MFsUsedPct, pct(d.Capacity-d.Free, d.Capacity), "mount", d.Path)
					add(proto.MFsFree, d.Free, "mount", d.Path)
					add(proto.MFsTotal, d.Capacity, "mount", d.Path)
				}
			}
		}
		if err := save("vm/"+v.Ref, kindVMWVM, v.Name, meta, samples); err != nil {
			return err
		}
	}

	known := s.st.PVEHostIDs(src.ID)
	if sum["vms"].(int) == 0 && hadVMWMachines(known) {
		// Хосты видны, а машин нет ни одной: скорее права выданы без наследования,
		// чем удалили всё сразу. Машины «удалёнными» не помечаем.
		msg := "VMware отдал хосты без единой машины: у пользователя, видимо, нет прав на машины. " +
			"Выдайте роль Read-only на корне vCenter с галкой «Propagate to children»"
		if s.vmw.warnOnce(src.ID) {
			log.Printf("vmware %s: %s", src.Name, msg)
		}
		s.st.VMWResult(src.ID, false, msg, sum)
		return nil
	}
	s.vmw.clearWarn(src.ID)
	for ext, id := range known {
		if !seen[ext] {
			s.st.MarkPVEGone(id)
			if h, err := s.st.Host(id); err == nil {
				s.eng.Eval(h, now)
			}
		}
	}
	s.st.VMWResult(src.ID, true, "", sum)
	return nil
}

func hadVMWMachines(known map[string]string) bool {
	for ext := range known {
		if strings.HasPrefix(ext, "vm/") {
			return true
		}
	}
	return false
}

// ---------- API ----------

func (s *Server) listVMW(w http.ResponseWriter, r *http.Request, _ *User) {
	srcs, err := s.st.VMWSources()
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, srcs)
}

func (s *Server) addVMW(w http.ResponseWriter, r *http.Request, _ *User) {
	var in struct {
		Name, URL, Username, Password, Fingerprint string
	}
	if err := readJSON(r, &in, 8192); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	base, err := vmware.NormalizeURL(in.URL)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	in.Username = strings.TrimSpace(in.Username)
	if in.Username == "" || len(in.Username) > 200 {
		httpErr(w, 400, "укажите пользователя, например sentra@vsphere.local (на ESXi — локальный пользователь)")
		return
	}
	if in.Password == "" || len(in.Password) > 256 {
		httpErr(w, 400, "укажите пароль пользователя")
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = strings.TrimPrefix(base, "https://")
	}
	if len([]rune(name)) > 100 {
		httpErr(w, 400, "название до 100 символов")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	fp := strings.ToUpper(strings.TrimSpace(in.Fingerprint))
	if fp == "" {
		// первое подключение: запоминаем сертификат, который показал сервер
		if fp, err = vmware.FetchFingerprint(ctx, base); err != nil {
			httpErr(w, 502, err.Error())
			return
		}
	}
	cl := vmware.New(base, in.Username, in.Password, fp)
	defer cl.Logout(context.WithoutCancel(ctx))
	about, err := cl.About(ctx)
	if err != nil {
		if errors.Is(err, vmware.ErrFingerprint) {
			err = errors.New("отпечаток сертификата не совпадает с указанным: проверьте его в vSphere Client")
		}
		httpErr(w, 502, err.Error())
		return
	}
	inv, err := cl.Inventory(ctx)
	if err != nil {
		httpErr(w, 502, err.Error())
		return
	}
	if len(inv.Hosts) == 0 {
		httpErr(w, 502, "вход выполнен, но пользователь не видит ни одного хоста: выдайте ему роль Read-only на корне с галкой «Propagate to children»")
		return
	}
	vms := 0
	for _, v := range inv.VMs {
		if !v.Template {
			vms++
		}
	}
	id, err := s.st.AddVMW(name, base, in.Username, in.Password, fp, about.APIType)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	select {
	case s.vmw.kick <- id:
	default:
	}
	log.Printf("подключён VMware %s (%s), %s: хостов %d, ВМ %d", name, base, about.FullName, len(inv.Hosts), vms)
	writeJSON(w, 200, map[string]any{"id": id, "name": name, "product": about.FullName, "vcenter": about.IsVCenter(),
		"fingerprint": fp, "hosts": len(inv.Hosts), "vms": vms})
}

func (s *Server) deleteVMW(w http.ResponseWriter, r *http.Request, _ *User) {
	id := r.PathValue("id")
	s.vmw.drop(id)
	if err := s.st.DeleteVMW(id); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// trustVMW принимает новый сертификат (после перевыпуска или переустановки).
func (s *Server) trustVMW(w http.ResponseWriter, r *http.Request, _ *User) {
	src, err := s.st.VMWSource(r.PathValue("id"))
	if err != nil {
		httpErr(w, 404, "подключение не найдено")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	fp, err := vmware.FetchFingerprint(ctx, src.URL)
	if err != nil {
		httpErr(w, 502, err.Error())
		return
	}
	cl := vmware.New(src.URL, src.Username, src.Password, fp)
	if _, err := cl.About(ctx); err != nil {
		httpErr(w, 502, err.Error())
		return
	}
	cl.Logout(ctx)
	s.vmw.drop(src.ID)
	s.st.SetVMWFingerprint(src.ID, fp)
	select {
	case s.vmw.kick <- src.ID:
	default:
	}
	writeJSON(w, 200, map[string]string{"fingerprint": fp})
}
