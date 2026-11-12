// sentra-pvemock — поддельный Proxmox VE для проверки Sentra на большом парке.
// Отвечает на те же запросы API, что и настоящий Proxmox: /version,
// /cluster/resources и /nodes/{узел}/status. Нагрузка машин меняется сама.
//
//	sentra-pvemock -nodes 4 -vms 120
//
// В Sentra: «Добавить › Proxmox», адрес 127.0.0.1:8006 (или адрес машины, где
// запущена заглушка), ID токена и секрет любые.
//
// Управление сценариями (в соседнем терминале):
//
//	curl -k 'https://127.0.0.1:8006/mock'                     что сейчас происходит
//	curl -k 'https://127.0.0.1:8006/mock?vm=105&cpu=98'       машина 105 грузит CPU на 98%
//	curl -k 'https://127.0.0.1:8006/mock?vm=105&mem=93'       память машины 105 занята на 93%
//	curl -k 'https://127.0.0.1:8006/mock?vm=105&stop=1'       выключить машину (start=1 — включить)
//	curl -k 'https://127.0.0.1:8006/mock?vm=105&gone=1'       машина пропала из кластера
//	curl -k 'https://127.0.0.1:8006/mock?node=pve2&offline=1' узел выпал из кластера (online=1 — вернуть)
//	curl -k 'https://127.0.0.1:8006/mock?vm=105&disk=95'      диск / внутри ВМ 105 заполнен на 95% (через гостевой агент)
//	curl -k 'https://127.0.0.1:8006/mock?vm=105&agent=0'      у ВМ 105 нет гостевого агента (agent=1 — есть)
//	curl -k 'https://127.0.0.1:8006/mock?forbid=1'            у токена нет права на гостевой агент (forbid=0 — есть)
//	curl -k 'https://127.0.0.1:8006/mock?reset=1'             убрать все сценарии
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"log"
	"math"
	"math/big"
	mrand "math/rand"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const gib = 1 << 30

type vm struct {
	id      int
	node    string
	kind    string // qemu или lxc
	name    string
	cores   int
	maxmem  float64
	maxdisk float64
	disk    float64
	running bool
	gone    bool
	cpu     float64 // доля 0..1, плавает около base
	base    float64
	mem     float64 // доля 0..1
	memBase float64
	pinCPU  float64 // сценарий: зафиксированная загрузка; <0 — нет
	pinMem  float64
	netIn   float64
	netOut  float64
	started time.Time
	agent   bool    // в ВМ работает гостевой агент QEMU
	windows bool    // гостевая ОС: диски C: и D: вместо / и /var
	fsRoot  float64 // заполнение главного раздела, доля
	fsData  float64
	pinDisk float64
}

type node struct {
	name    string
	offline bool
	cpus    int
	maxmem  float64
	root    float64 // занято на корневом разделе, доля
	storage []storage
}

type storage struct {
	name, typ string
	max, used float64
	shared    bool
}

type cluster struct {
	mu      sync.Mutex
	nodes   []*node
	vms     []*vm
	byID    map[int]*vm
	rnd     *mrand.Rand
	delay   time.Duration
	started time.Time
	calls   map[string]int
	forbid  bool // токену не выдано право на гостевой агент
}

var roles = []string{"web", "db", "app", "1c", "rdp", "file", "mail", "dc", "backup", "dev", "test", "vpn", "mon", "crm", "git", "proxy"}

func newCluster(nodes, vms int, seed int64, stopped float64) *cluster {
	r := mrand.New(mrand.NewSource(seed))
	c := &cluster{rnd: r, byID: map[int]*vm{}, started: time.Now(), calls: map[string]int{}}
	for i := 1; i <= nodes; i++ {
		c.nodes = append(c.nodes, &node{
			name: fmt.Sprintf("pve%d", i), cpus: 96, maxmem: 512 * gib, root: 0.2 + r.Float64()*0.3,
			storage: []storage{
				{"local", "dir", 100 * gib, (20 + r.Float64()*40) * gib, false},
				{"local-lvm", "lvmthin", 2000 * gib, (500 + r.Float64()*900) * gib, false},
			},
		})
	}
	for i := 0; i < vms; i++ {
		n := c.nodes[i%nodes]
		v := &vm{
			id: 100 + i, node: n.name, kind: "qemu", cores: []int{1, 2, 4, 8}[r.Intn(4)],
			maxmem: float64([]int{2, 4, 8, 16}[r.Intn(4)]) * gib, maxdisk: float64([]int{32, 64, 128}[r.Intn(3)]) * gib,
			running: r.Float64() >= stopped, base: 0.02 + r.Float64()*0.35, memBase: 0.2 + r.Float64()*0.55,
			pinCPU: -1, pinMem: -1, started: time.Now().Add(-time.Duration(r.Intn(40*24)) * time.Hour),
		}
		v.agent = r.Float64() < 0.7
		v.windows = r.Float64() < 0.25
		v.fsRoot, v.fsData, v.pinDisk = 0.2+r.Float64()*0.6, 0.1+r.Float64()*0.7, -1
		if r.Float64() < 0.25 {
			v.kind = "lxc"
			v.disk = v.maxdisk * (0.1 + r.Float64()*0.6) // у контейнеров Proxmox знает заполнение диска
		}
		v.name = fmt.Sprintf("%s-%d", roles[r.Intn(len(roles))], v.id)
		v.cpu, v.mem = v.base, v.memBase
		c.vms = append(c.vms, v)
		c.byID[v.id] = v
	}
	return c
}

// tick двигает нагрузку: случайное блуждание вокруг базового уровня.
func (c *cluster) tick(dt float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, v := range c.vms {
		if !v.running {
			continue
		}
		v.cpu = clamp(v.cpu+(v.base-v.cpu)*0.3+(c.rnd.Float64()-0.5)*0.1, 0.005, 1)
		v.mem = clamp(v.mem+(v.memBase-v.mem)*0.2+(c.rnd.Float64()-0.5)*0.02, 0.05, 0.99)
		if v.pinCPU >= 0 {
			v.cpu = v.pinCPU
		}
		if v.pinMem >= 0 {
			v.mem = v.pinMem
		}
		v.netIn += (10e3 + c.rnd.Float64()*500e3) * dt
		v.netOut += (5e3 + c.rnd.Float64()*300e3) * dt
	}
}

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

func (c *cluster) nodeByName(name string) *node {
	for _, n := range c.nodes {
		if n.name == name {
			return n
		}
	}
	return nil
}

func (c *cluster) resources() []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []map[string]any
	load := map[string][2]float64{} // узел → занятые ядра, память
	for _, v := range c.vms {
		if v.gone {
			continue
		}
		n := c.nodeByName(v.node)
		r := map[string]any{
			"id": fmt.Sprintf("%s/%d", v.kind, v.id), "type": v.kind, "node": v.node, "vmid": v.id, "name": v.name,
			"maxcpu": v.cores, "maxmem": v.maxmem, "maxdisk": v.maxdisk, "template": 0, "tags": "",
		}
		switch {
		case n.offline:
			r["status"] = "unknown" // так Proxmox показывает машины узла, который выпал из кластера
		case v.running:
			r["status"] = "running"
			r["cpu"] = v.cpu
			r["mem"] = math.Round(v.mem * v.maxmem)
			r["netin"], r["netout"] = math.Round(v.netIn), math.Round(v.netOut)
			r["uptime"] = int(time.Since(v.started).Seconds())
			if v.kind == "lxc" {
				r["disk"] = math.Round(v.disk)
			} else {
				r["disk"] = 0 // у ВМ без гостевого агента Proxmox заполнение диска не знает
			}
			l := load[v.node]
			load[v.node] = [2]float64{l[0] + v.cpu*float64(v.cores), l[1] + v.mem*v.maxmem}
		default:
			r["status"] = "stopped"
			r["cpu"], r["mem"], r["disk"], r["uptime"] = 0, 0, 0, 0
		}
		out = append(out, r)
	}
	for _, n := range c.nodes {
		l := load[n.name]
		r := map[string]any{"id": "node/" + n.name, "type": "node", "node": n.name, "maxcpu": n.cpus, "maxmem": n.maxmem,
			"maxdisk": 100 * gib, "disk": math.Round(n.root * 100 * gib)}
		if n.offline {
			r["status"] = "offline"
		} else {
			r["status"] = "online"
			r["cpu"] = clamp(l[0]/float64(n.cpus)+0.03, 0, 1)
			r["mem"] = math.Round(math.Min(l[1]+12*gib, n.maxmem))
			r["uptime"] = int(time.Since(c.started).Seconds()) + 86400*41
		}
		out = append(out, r)
		for _, s := range n.storage {
			sr := map[string]any{"id": "storage/" + n.name + "/" + s.name, "type": "storage", "node": n.name, "storage": s.name,
				"plugintype": s.typ, "shared": 0, "maxdisk": s.max, "disk": math.Round(s.used), "status": "available"}
			if n.offline {
				sr["status"] = "unknown"
			}
			out = append(out, sr)
		}
	}
	return out
}

func (c *cluster) nodeStatus(name string) (map[string]any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.nodeByName(name)
	if n == nil || n.offline {
		return nil, false
	}
	var mem float64
	for _, v := range c.vms {
		if v.node == name && v.running && !v.gone {
			mem += v.mem * v.maxmem
		}
	}
	mem = math.Min(mem+12*gib, n.maxmem)
	return map[string]any{
		"loadavg":    []string{"1.20", "1.05", "0.98"},
		"memory":     map[string]any{"total": n.maxmem, "used": mem, "free": n.maxmem - mem},
		"swap":       map[string]any{"total": 8 * gib, "used": 0},
		"rootfs":     map[string]any{"total": 100 * gib, "used": n.root * 100 * gib, "avail": (1 - n.root) * 100 * gib},
		"pveversion": "pve-manager/8.2.4/faa83925c9641325",
		"kversion":   "Linux 6.8.8-2-pve #1 SMP PREEMPT_DYNAMIC",
		"cpuinfo":    map[string]any{"model": "Intel(R) Xeon(R) Silver 4314 CPU @ 2.40GHz", "cpus": n.cpus},
		"uptime":     int(time.Since(c.started).Seconds()) + 86400*41,
	}, true
}

// control меняет сценарий: ?vm=105&cpu=98, ?vm=105&stop=1, ?node=pve2&offline=1, ?reset=1.
func (c *cluster) control(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	c.mu.Lock()
	msg := []string{}
	if q.Get("reset") != "" {
		for _, v := range c.vms {
			v.pinCPU, v.pinMem, v.pinDisk, v.gone = -1, -1, -1, false
		}
		c.forbid = false
		for _, n := range c.nodes {
			n.offline = false
		}
		msg = append(msg, "все сценарии сброшены")
	}
	if id, err := strconv.Atoi(q.Get("vm")); err == nil {
		v := c.byID[id]
		if v == nil {
			c.mu.Unlock()
			http.Error(w, "нет машины "+q.Get("vm"), 404)
			return
		}
		if p, err := strconv.ParseFloat(q.Get("cpu"), 64); err == nil {
			v.pinCPU = clamp(p/100, 0, 1)
			if p < 0 {
				v.pinCPU = -1
			}
			msg = append(msg, fmt.Sprintf("%s: CPU %s%%", v.name, q.Get("cpu")))
		}
		if p, err := strconv.ParseFloat(q.Get("mem"), 64); err == nil {
			v.pinMem = clamp(p/100, 0, 0.999)
			if p < 0 {
				v.pinMem = -1
			}
			msg = append(msg, fmt.Sprintf("%s: память %s%%", v.name, q.Get("mem")))
		}
		if p, err := strconv.ParseFloat(q.Get("disk"), 64); err == nil {
			v.pinDisk = clamp(p/100, 0, 1)
			if p < 0 {
				v.pinDisk = -1
			}
			msg = append(msg, fmt.Sprintf("%s: главный раздел %s%%", v.name, q.Get("disk")))
		}
		if a := q.Get("agent"); a != "" {
			v.agent = a != "0"
			msg = append(msg, fmt.Sprintf("%s: гостевой агент = %v", v.name, v.agent))
		}
		if q.Get("stop") != "" {
			v.running = false
			msg = append(msg, v.name+": выключена")
		}
		if q.Get("start") != "" {
			v.running, v.started = true, time.Now()
			msg = append(msg, v.name+": включена")
		}
		if q.Get("gone") != "" {
			v.gone = q.Get("gone") != "0"
			msg = append(msg, fmt.Sprintf("%s: пропала из кластера = %v", v.name, v.gone))
		}
	}
	if f := q.Get("forbid"); f != "" {
		c.forbid = f != "0"
		msg = append(msg, fmt.Sprintf("нет права на гостевой агент = %v", c.forbid))
	}
	if n := c.nodeByName(q.Get("node")); n != nil {
		if q.Get("offline") != "" {
			n.offline = true
			msg = append(msg, n.name+": не в сети")
		}
		if q.Get("online") != "" {
			n.offline = false
			msg = append(msg, n.name+": снова в сети")
		}
	}
	// сводка
	var pinned []string
	running := 0
	for _, v := range c.vms {
		if v.running && !v.gone {
			running++
		}
		if v.pinCPU >= 0 || v.pinMem >= 0 || v.pinDisk >= 0 || v.gone {
			pinned = append(pinned, fmt.Sprintf("  %d %s: cpu=%v mem=%v disk=%v gone=%v running=%v", v.id, v.name, pct(v.pinCPU), pct(v.pinMem), pct(v.pinDisk), v.gone, v.running))
		}
	}
	var off []string
	for _, n := range c.nodes {
		if n.offline {
			off = append(off, n.name)
		}
	}
	calls := make([]string, 0, len(c.calls))
	for k, n := range c.calls {
		calls = append(calls, fmt.Sprintf("  %s: %d", k, n))
	}
	c.mu.Unlock()
	sort.Strings(pinned)
	sort.Strings(calls)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	for _, m := range msg {
		fmt.Fprintln(w, "→", m)
	}
	agents := 0
	for _, v := range c.vms {
		if v.kind == "qemu" && v.agent {
			agents++
		}
	}
	fmt.Fprintf(w, "узлов: %d, машин: %d, запущено: %d, ВМ с гостевым агентом: %d, узлы не в сети: %v, нет права на агент: %v\n", len(c.nodes), len(c.vms), running, agents, off, c.forbid)
	if len(pinned) > 0 {
		fmt.Fprintln(w, "сценарии:")
		fmt.Fprintln(w, strings.Join(pinned, "\n"))
	}
	fmt.Fprintln(w, "запросов к API:")
	fmt.Fprintln(w, strings.Join(calls, "\n"))
}

func pct(v float64) string {
	if v < 0 {
		return "авто"
	}
	return fmt.Sprintf("%.0f%%", v*100)
}

func (c *cluster) api(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Authorization"), "PVEAPIToken=") {
		http.Error(w, "authentication failure", 401)
		return
	}
	if c.delay > 0 {
		time.Sleep(c.delay)
	}
	path := strings.TrimPrefix(r.URL.Path, "/api2/json")
	var data any
	key := path
	switch {
	case path == "/version":
		data = map[string]any{"version": "8.2.4", "release": "8.2", "repoid": "faa83925"}
	case path == "/cluster/resources":
		data = c.resources()
	case strings.HasPrefix(path, "/nodes/") && strings.HasSuffix(path, "/agent/get-fsinfo"):
		key = "/nodes/*/qemu/*/agent/get-fsinfo"
		c.mu.Lock()
		c.calls[key]++
		c.mu.Unlock()
		res, code, msg := c.fsinfo(path)
		if code != 200 {
			http.Error(w, msg, code)
			return
		}
		data = map[string]any{"result": res}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"data": data})
		return
	case strings.HasPrefix(path, "/nodes/") && strings.HasSuffix(path, "/status"):
		key = "/nodes/*/status"
		st, ok := c.nodeStatus(strings.TrimSuffix(strings.TrimPrefix(path, "/nodes/"), "/status"))
		if !ok {
			http.Error(w, "no such node or node offline", 595)
			return
		}
		data = st
	default:
		http.Error(w, "not implemented in mock", 501)
		return
	}
	c.mu.Lock()
	c.calls[key]++
	c.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"data": data})
}

// loadOrCreateCert хранит сертификат в файле. Sentra запоминает отпечаток
// сертификата Proxmox, и новый сертификат после перезапуска заглушки она
// справедливо отвергла бы как подмену.
func loadOrCreateCert(path string) tls.Certificate {
	if path != "" {
		if c, err := tls.LoadX509KeyPair(path, path); err == nil {
			return c
		}
	}
	c := selfSigned()
	if path != "" {
		der, _ := x509.MarshalECPrivateKey(c.PrivateKey.(*ecdsa.PrivateKey))
		out := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Certificate[0]})
		out = append(out, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})...)
		if err := os.WriteFile(path, out, 0o600); err != nil {
			log.Printf("не удалось сохранить сертификат в %s: %v (после перезапуска отпечаток сменится)", path, err)
		}
	}
	return c
}

func defaultCertPath(listen string) string {
	dir := os.TempDir()
	if st, err := os.Stat("/var/lib/sentra"); err == nil && st.IsDir() {
		dir = "/var/lib/sentra" // в контейнере коробки: переживает перезапуск
	}
	port := listen[strings.LastIndex(listen, ":")+1:]
	return filepath.Join(dir, "pvemock-"+port+".pem")
}

// fsinfo отвечает как гостевой агент: /nodes/{узел}/qemu/{vmid}/agent/get-fsinfo.
func (c *cluster) fsinfo(path string) (any, int, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.forbid {
		return nil, 403, "Permission check failed (/vms/..., VM.GuestAgent.Audit)"
	}
	p := strings.Split(strings.Trim(path, "/"), "/") // nodes, узел, qemu, vmid, agent, get-fsinfo
	if len(p) != 6 {
		return nil, 400, "bad path"
	}
	id, _ := strconv.Atoi(p[3])
	v := c.byID[id]
	if v == nil || v.gone || v.node != p[1] || v.kind != "qemu" {
		return nil, 500, fmt.Sprintf("Configuration file 'nodes/%s/qemu-server/%s.conf' does not exist", p[1], p[3])
	}
	if !v.running {
		return nil, 500, fmt.Sprintf("VM %d is not running", id)
	}
	if !v.agent {
		return nil, 500, "No QEMU guest agent configured"
	}
	root := v.fsRoot
	if v.pinDisk >= 0 {
		root = v.pinDisk
	}
	fs := func(name, mp, typ string, total, used float64) map[string]any {
		return map[string]any{"name": name, "mountpoint": mp, "type": typ, "total-bytes": int64(total), "used-bytes": int64(used),
			"disk": []map[string]any{{"bus-type": "scsi", "target": 0, "unit": 0}}}
	}
	if v.windows {
		return []map[string]any{
			fs("\\\\?\\Volume{1}", "System Reserved", "NTFS", 0.5*gib, 0.03*gib),
			fs("\\\\?\\Volume{2}", "C:\\", "NTFS", v.maxdisk*0.6, v.maxdisk*0.6*root),
			fs("\\\\?\\Volume{3}", "D:\\", "NTFS", v.maxdisk*0.4, v.maxdisk*0.4*v.fsData),
		}, 200, ""
	}
	return []map[string]any{
		fs("sda1", "/", "ext4", v.maxdisk*0.7, v.maxdisk*0.7*root),
		fs("sda1", "/var/lib/docker/overlay2/x/merged", "overlay", v.maxdisk*0.7, v.maxdisk*0.7*root),
		fs("sda2", "/boot", "ext4", 1*gib, 0.25*gib),
		fs("sdb1", "/var/lib/data", "xfs", v.maxdisk*0.3, v.maxdisk*0.3*v.fsData),
		fs("loop0", "/snap/core22/1380", "squashfs", 0.08*gib, 0.08*gib),
	}, 200, ""
}

func selfSigned() tls.Certificate {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "pve-mock"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(1, 0, 0),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, _ := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func main() {
	listen := flag.String("listen", ":8006", "адрес и порт")
	nodes := flag.Int("nodes", 4, "сколько узлов")
	vms := flag.Int("vms", 120, "сколько машин всего (ВМ и контейнеры)")
	stopped := flag.Float64("stopped", 0.12, "доля выключенных машин")
	seed := flag.Int64("seed", 7, "зерно случайных чисел: одинаковое зерно — одинаковый кластер")
	delay := flag.Duration("delay", 0, "задержка ответа API, например 300ms: как у медленного кластера")
	certPath := flag.String("cert", "", "файл сертификата (по умолчанию /var/lib/sentra/pvemock-<порт>.pem или во временной папке)")
	flag.Parse()
	if *certPath == "" {
		*certPath = defaultCertPath(*listen)
	}
	if *nodes < 1 || *vms < 0 {
		log.Fatal("нужен хотя бы один узел")
	}

	c := newCluster(*nodes, *vms, *seed, *stopped)
	c.delay = *delay
	go func() {
		last := time.Now()
		for range time.Tick(5 * time.Second) {
			now := time.Now()
			c.tick(now.Sub(last).Seconds())
			last = now
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/", c.api)
	mux.HandleFunc("/mock", c.control)
	srv := &http.Server{Addr: *listen, Handler: mux, TLSConfig: &tls.Config{Certificates: []tls.Certificate{loadOrCreateCert(*certPath)}}}
	log.Printf("поддельный Proxmox: %d узлов, %d машин, слушаю https://%s", *nodes, *vms, strings.TrimPrefix(*listen, ":"))
	log.Printf("в Sentra: «Добавить › Proxmox», адрес этой машины и порт %s, ID токена и секрет любые", strings.TrimPrefix(*listen, ":"))
	log.Fatal(srv.ListenAndServeTLS("", ""))
}
