package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"sentra/internal/proto"
	"sentra/internal/proxmox"
)

const pvePollEvery = 30 * time.Second

// pvePoller опрашивает все подключения Proxmox и хранит прошлые счётчики сети.
type pvePoller struct {
	mu   sync.Mutex
	prev map[string]netPrev // host id → прошлые netin/netout
	kick chan string        // id подключения, которое нужно опросить сейчас
}

type netPrev struct {
	in, out float64
	at      time.Time
}

func newPVEPoller() *pvePoller {
	return &pvePoller{prev: map[string]netPrev{}, kick: make(chan string, 8)}
}

// StartPollers запускает фоновый опрос Proxmox. Останавливается с ctx.
func (s *Server) StartPollers(ctx context.Context) {
	go func() {
		t := time.NewTicker(pvePollEvery)
		defer t.Stop()
		s.pollAllPVE(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.pollAllPVE(ctx)
			case id := <-s.pve.kick:
				if src, err := s.st.PVESource(id); err == nil {
					s.pollPVE(ctx, src)
				}
			}
		}
	}()
}

func (s *Server) pollAllPVE(ctx context.Context) {
	srcs, err := s.st.PVESources()
	if err != nil {
		log.Printf("proxmox: список подключений: %v", err)
		return
	}
	var wg sync.WaitGroup
	for _, src := range srcs {
		wg.Add(1)
		go func(src *PVESource) { defer wg.Done(); s.pollPVE(ctx, src) }(src)
	}
	wg.Wait()
}

func (s *Server) pollPVE(ctx context.Context, src *PVESource) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	if err := s.collectPVE(ctx, src); err != nil {
		if err.Error() != src.LastError {
			log.Printf("proxmox %s (%s): %v", src.Name, src.URL, err)
		}
		s.st.PVEResult(src.ID, false, err.Error(), nil)
	}
}

func (s *Server) collectPVE(ctx context.Context, src *PVESource) error {
	cl := proxmox.New(src.URL, src.TokenID, src.Secret, src.Fingerprint)
	res, err := cl.Resources(ctx)
	if err != nil {
		return err
	}
	now := time.Now()
	ts := now.UnixMilli()

	storages := map[string][]proxmox.Resource{} // узел → хранилища
	nodes := map[string]proxmox.Resource{}
	for _, r := range res {
		switch r.Type {
		case "storage":
			if r.Status == "available" && r.MaxDisk > 0 {
				storages[r.Node] = append(storages[r.Node], r)
			}
		case "node":
			nodes[r.Node] = r
		}
	}

	seen := map[string]bool{}
	sum := map[string]any{"nodes": 0, "qemu": 0, "lxc": 0, "running": 0}
	inc := func(k string) { sum[k] = sum[k].(int) + 1 }

	for _, r := range res {
		var kind string
		switch r.Type {
		case "node":
			kind = "pve-node"
		case "qemu":
			kind = "pve-qemu"
		case "lxc":
			kind = "pve-lxc"
		default:
			continue
		}
		if r.Template == 1 {
			continue // шаблоны не машины
		}
		seen[r.ID] = true
		meta := map[string]any{
			"source_name": src.Name, "node": r.Node, "type": r.Type, "status": r.Status,
			"maxcpu": float64(r.MaxCPU), "maxmem": float64(r.MaxMem), "maxdisk": float64(r.MaxDisk),
			"uptime": float64(r.Uptime),
		}
		name := r.Name
		var samples []proto.Sample
		add := func(n string, v float64, kv ...string) {
			var l map[string]string
			if len(kv) == 2 {
				l = map[string]string{kv[0]: kv[1]}
			}
			samples = append(samples, proto.Sample{Name: n, Labels: l, Value: v})
		}

		if kind == "pve-node" {
			name = r.Node
			inc("nodes")
			if r.Status == "online" {
				add(proto.MCPUUsage, round2(float64(r.CPU)*100))
				add(proto.MUptime, float64(r.Uptime))
				if st, err := cl.NodeStatus(ctx, r.Node); err == nil {
					meta["pve_version"] = pveVersion(st.PVEVersion)
					meta["kernel"] = st.KVersion
					meta["cpu_model"] = st.CPUInfo.Model
					if len(st.LoadAvg) == 3 {
						add(proto.MLoad1, float64(st.LoadAvg[0]))
						add(proto.MLoad5, float64(st.LoadAvg[1]))
						add(proto.MLoad15, float64(st.LoadAvg[2]))
					}
					if st.Memory.Total > 0 {
						add(proto.MMemTotal, float64(st.Memory.Total))
						add(proto.MMemUsed, float64(st.Memory.Used))
						add(proto.MMemUsedPct, round2(100*float64(st.Memory.Used)/float64(st.Memory.Total)))
					}
					if st.Swap.Total > 0 {
						add(proto.MSwapUsedPct, round2(100*float64(st.Swap.Used)/float64(st.Swap.Total)))
					} else {
						add(proto.MSwapUsedPct, 0)
					}
					if st.RootFS.Total > 0 {
						add(proto.MFsUsedPct, round2(100*float64(st.RootFS.Used)/float64(st.RootFS.Total)), "mount", "/ (система)")
						add(proto.MFsFree, float64(st.RootFS.Avail), "mount", "/ (система)")
						add(proto.MFsTotal, float64(st.RootFS.Total), "mount", "/ (система)")
					}
				} else if r.MaxMem > 0 { // нет прав на status: берём что есть
					add(proto.MMemUsedPct, round2(100*float64(r.Mem)/float64(r.MaxMem)))
				}
				var stList []map[string]any
				for _, sr := range storages[r.Node] {
					pct := round2(100 * float64(sr.Disk) / float64(sr.MaxDisk))
					add(proto.MFsUsedPct, pct, "mount", sr.Storage)
					add(proto.MFsFree, float64(sr.MaxDisk-sr.Disk), "mount", sr.Storage)
					add(proto.MFsTotal, float64(sr.MaxDisk), "mount", sr.Storage)
					stList = append(stList, map[string]any{"name": sr.Storage, "type": sr.PlugType,
						"used": float64(sr.Disk), "total": float64(sr.MaxDisk), "percent": pct, "shared": sr.Shared == 1})
				}
				meta["storages"] = stList
			}
		} else {
			inc(r.Type)
			meta["vmid"] = int(r.VMID)
			if r.Tags != "" {
				meta["tags"] = strings.Split(r.Tags, ";")
			}
			if name == "" {
				name = fmt.Sprintf("%s %d", map[string]string{"qemu": "VM", "lxc": "CT"}[r.Type], int(r.VMID))
			}
			if n, ok := nodes[r.Node]; ok && n.Status != "online" {
				meta["node_offline"] = true
			}
			if r.Status == "running" {
				inc("running")
				add(proto.MCPUUsage, round2(float64(r.CPU)*100))
				add(proto.MUptime, float64(r.Uptime))
				if r.MaxMem > 0 {
					add(proto.MMemTotal, float64(r.MaxMem))
					add(proto.MMemUsed, float64(r.Mem))
					add(proto.MMemUsedPct, round2(100*float64(r.Mem)/float64(r.MaxMem)))
				}
				// у QEMU заполнение диска видно только через гостевой агент: disk=0
				if r.Disk > 0 && r.MaxDisk > 0 {
					add(proto.MFsUsedPct, round2(100*float64(r.Disk)/float64(r.MaxDisk)), "mount", "/")
					add(proto.MFsFree, float64(r.MaxDisk-r.Disk), "mount", "/")
					add(proto.MFsTotal, float64(r.MaxDisk), "mount", "/")
				}
			}
		}
		meta["pve_name"] = name

		id, err := s.st.UpsertPVEHost(src.ID, r.ID, kind, name, now.Unix(), nil, meta)
		if err != nil {
			return fmt.Errorf("сохранение %s: %w", r.ID, err)
		}
		// сеть: Proxmox отдаёт счётчики с запуска машины, считаем скорость сами
		if kind != "pve-node" && r.Status == "running" {
			s.pve.mu.Lock()
			p, ok := s.pve.prev[id]
			s.pve.prev[id] = netPrev{in: float64(r.NetIn), out: float64(r.NetOut), at: now}
			s.pve.mu.Unlock()
			if dt := now.Sub(p.at).Seconds(); ok && dt > 1 && float64(r.NetIn) >= p.in && float64(r.NetOut) >= p.out {
				add(proto.MNetRx, round2((float64(r.NetIn)-p.in)/dt), "iface", "")
				add(proto.MNetTx, round2((float64(r.NetOut)-p.out)/dt), "iface", "")
			}
		}
		if len(samples) > 0 {
			if err := s.vm.Import(ctx, id, ts, samples); err != nil {
				return fmt.Errorf("хранилище метрик: %w", err)
			}
		}
		s.st.UpsertPVEHost(src.ID, r.ID, kind, name, now.Unix(), summarize(samples), meta)
		if h, err := s.st.Host(id); err == nil {
			s.eng.Eval(h, now)
		}
	}

	for ext, id := range s.st.PVEHostIDs(src.ID) {
		if !seen[ext] {
			s.st.MarkPVEGone(id)
			if h, err := s.st.Host(id); err == nil {
				s.eng.Eval(h, now)
			}
		}
	}
	s.st.PVEResult(src.ID, true, "", sum)
	return nil
}

// pveVersion: "pve-manager/8.2.4/faa83925" → "8.2.4"
func pveVersion(s string) string {
	if p := strings.Split(s, "/"); len(p) >= 2 {
		return p[1]
	}
	return s
}

func round2(v float64) float64 {
	if v < 0 {
		v = 0
	}
	return float64(int64(v*100+0.5)) / 100
}

// ---------- API ----------

var tokenIDRe = regexp.MustCompile(`^[^@\s!]+@[^@\s!]+![A-Za-z0-9][A-Za-z0-9_.-]*$`)

func (s *Server) listPVE(w http.ResponseWriter, r *http.Request, _ *User) {
	srcs, err := s.st.PVESources()
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, srcs)
}

func (s *Server) addPVE(w http.ResponseWriter, r *http.Request, _ *User) {
	var in struct {
		Name, URL, TokenID, Secret, Fingerprint string
	}
	if err := readJSON(r, &in, 8192); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	base, err := proxmox.NormalizeURL(in.URL)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	in.TokenID = strings.TrimSpace(in.TokenID)
	in.Secret = strings.TrimSpace(in.Secret)
	if !tokenIDRe.MatchString(in.TokenID) {
		httpErr(w, 400, "ID токена пишется так: пользователь@realm!имя, например sentra@pve!monitor")
		return
	}
	if len(in.Secret) < 8 || len(in.Secret) > 128 {
		httpErr(w, 400, "укажите секрет токена: его показали один раз при создании токена")
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
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	fp := strings.ToUpper(strings.TrimSpace(in.Fingerprint))
	if fp == "" {
		// первое подключение: запоминаем сертификат, который показал сервер
		if fp, err = proxmox.FetchFingerprint(ctx, base); err != nil {
			httpErr(w, 502, err.Error())
			return
		}
	}
	cl := proxmox.New(base, in.TokenID, in.Secret, fp)
	ver, err := cl.Version(ctx)
	if err != nil {
		code := 502
		if errors.Is(err, proxmox.ErrFingerprint) {
			err = errors.New("отпечаток сертификата не совпадает с указанным: проверьте его в Proxmox (Узел › Система › Сертификаты)")
		}
		httpErr(w, code, err.Error())
		return
	}
	res, err := cl.Resources(ctx)
	if err != nil {
		httpErr(w, 502, err.Error())
		return
	}
	cnt := map[string]int{}
	for _, x := range res {
		if x.Template != 1 {
			cnt[x.Type]++
		}
	}
	id, err := s.st.AddPVE(name, base, in.TokenID, in.Secret, fp)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	select {
	case s.pve.kick <- id:
	default:
	}
	log.Printf("подключён Proxmox %s (%s), версия %s: узлов %d, ВМ %d, контейнеров %d", name, base, ver, cnt["node"], cnt["qemu"], cnt["lxc"])
	writeJSON(w, 200, map[string]any{"id": id, "name": name, "version": ver, "fingerprint": fp,
		"nodes": cnt["node"], "qemu": cnt["qemu"], "lxc": cnt["lxc"]})
}

func (s *Server) deletePVE(w http.ResponseWriter, r *http.Request, _ *User) {
	if err := s.st.DeletePVE(r.PathValue("id")); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// trustPVE принимает новый сертификат Proxmox (например, после его перевыпуска).
func (s *Server) trustPVE(w http.ResponseWriter, r *http.Request, _ *User) {
	src, err := s.st.PVESource(r.PathValue("id"))
	if err != nil {
		httpErr(w, 404, "подключение не найдено")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	fp, err := proxmox.FetchFingerprint(ctx, src.URL)
	if err != nil {
		httpErr(w, 502, err.Error())
		return
	}
	if _, err := proxmox.New(src.URL, src.TokenID, src.Secret, fp).Version(ctx); err != nil {
		httpErr(w, 502, err.Error())
		return
	}
	s.st.SetPVEFingerprint(src.ID, fp)
	select {
	case s.pve.kick <- src.ID:
	default:
	}
	writeJSON(w, 200, map[string]string{"fingerprint": fp})
}
