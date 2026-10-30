//go:build linux

package agent

import (
	"bufio"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"dozor/internal/proto"
)

// Collector хранит предыдущие значения счётчиков, чтобы считать скорости.
type Collector struct {
	prevCPU  cpuTimes
	prevNet  map[string]netCounters
	prevAt   time.Time
	hostProc string // корень /proc (для тестов и контейнеров)
}

type cpuTimes struct{ total, idle, iowait uint64 }
type netCounters struct{ rx, tx, errs uint64 }

func NewCollector() *Collector {
	p := os.Getenv("DOZOR_PROC")
	if p == "" {
		p = "/proc"
	}
	c := &Collector{prevNet: map[string]netCounters{}, hostProc: p}
	c.prevCPU, _ = c.readCPU()
	c.prevNet = c.readNet()
	c.prevAt = time.Now()
	return c
}

func (c *Collector) Collect() []proto.Sample {
	var s []proto.Sample
	add := func(name string, v float64, kv ...string) {
		var l map[string]string
		if len(kv) > 0 {
			l = map[string]string{}
			for i := 0; i+1 < len(kv); i += 2 {
				l[kv[i]] = kv[i+1]
			}
		}
		s = append(s, proto.Sample{Name: name, Labels: l, Value: v})
	}
	now := time.Now()
	dt := now.Sub(c.prevAt).Seconds()

	// CPU
	if cur, err := c.readCPU(); err == nil {
		dTotal := float64(cur.total - c.prevCPU.total)
		if dTotal > 0 {
			idle := float64(cur.idle - c.prevCPU.idle)
			io := float64(cur.iowait - c.prevCPU.iowait)
			add(proto.MCPUUsage, round2(100*(dTotal-idle-io)/dTotal))
			add(proto.MCPUIOWait, round2(100*io/dTotal))
		}
		c.prevCPU = cur
	}

	// Load average
	if b, err := os.ReadFile(c.hostProc + "/loadavg"); err == nil {
		f := strings.Fields(string(b))
		if len(f) >= 4 {
			add(proto.MLoad1, atof(f[0]))
			add(proto.MLoad5, atof(f[1]))
			add(proto.MLoad15, atof(f[2]))
			if p := strings.Split(f[3], "/"); len(p) == 2 {
				add(proto.MProcs, atof(p[1]))
			}
		}
	}

	// Память
	if m := c.readMeminfo(); m["MemTotal"] > 0 {
		total := m["MemTotal"]
		avail, ok := m["MemAvailable"]
		if !ok {
			avail = m["MemFree"] + m["Buffers"] + m["Cached"]
		}
		used := total - avail
		add(proto.MMemTotal, float64(total))
		add(proto.MMemUsed, float64(used))
		add(proto.MMemUsedPct, round2(100*float64(used)/float64(total)))
		if st := m["SwapTotal"]; st > 0 {
			add(proto.MSwapUsedPct, round2(100*float64(st-m["SwapFree"])/float64(st)))
		} else {
			add(proto.MSwapUsedPct, 0)
		}
	}

	// Файловые системы
	for _, fs := range c.mounts() {
		var st syscall.Statfs_t
		if err := syscall.Statfs(fs, &st); err != nil {
			continue
		}
		total := st.Blocks * uint64(st.Bsize)
		if total == 0 {
			continue
		}
		free := st.Bavail * uint64(st.Bsize)
		usedBlocks := st.Blocks - st.Bfree
		// как в df: used / (used + avail)
		pct := 100 * float64(usedBlocks) / float64(usedBlocks+st.Bavail)
		add(proto.MFsTotal, float64(total), "mount", fs)
		add(proto.MFsFree, float64(free), "mount", fs)
		add(proto.MFsUsedPct, round2(pct), "mount", fs)
	}

	// Сеть
	cur := c.readNet()
	if dt > 0 {
		for name, n := range cur {
			p, ok := c.prevNet[name]
			if !ok || n.rx < p.rx || n.tx < p.tx {
				continue
			}
			add(proto.MNetRx, round2(float64(n.rx-p.rx)/dt), "iface", name)
			add(proto.MNetTx, round2(float64(n.tx-p.tx)/dt), "iface", name)
			if n.errs >= p.errs {
				add(proto.MNetErrors, round2(float64(n.errs-p.errs)/dt), "iface", name)
			}
		}
	}
	c.prevNet = cur

	if b, err := os.ReadFile(c.hostProc + "/uptime"); err == nil {
		if f := strings.Fields(string(b)); len(f) > 0 {
			add(proto.MUptime, float64(int64(atof(f[0]))))
		}
	}

	c.prevAt = now
	return s
}

func (c *Collector) readCPU() (cpuTimes, error) {
	f, err := os.Open(c.hostProc + "/stat")
	if err != nil {
		return cpuTimes{}, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)[1:]
		var t cpuTimes
		for i, v := range fields {
			n, _ := strconv.ParseUint(v, 10, 64)
			if i >= 8 { // guest и guest_nice уже входят в user/nice
				break
			}
			t.total += n
			switch i {
			case 3:
				t.idle = n
			case 4:
				t.iowait = n
			}
		}
		return t, nil
	}
	return cpuTimes{}, os.ErrNotExist
}

func (c *Collector) readMeminfo() map[string]uint64 {
	m := map[string]uint64{}
	f, err := os.Open(c.hostProc + "/meminfo")
	if err != nil {
		return m
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		fl := strings.Fields(rest)
		if len(fl) == 0 {
			continue
		}
		v, _ := strconv.ParseUint(fl[0], 10, 64)
		if len(fl) > 1 && fl[1] == "kB" {
			v *= 1024
		}
		m[k] = v
	}
	return m
}

var realFS = map[string]bool{
	"ext2": true, "ext3": true, "ext4": true, "xfs": true, "btrfs": true, "zfs": true,
	"f2fs": true, "jfs": true, "reiserfs": true, "vfat": true, "exfat": true, "ntfs": true,
	"ntfs3": true, "fuseblk": true, "nfs": true, "nfs4": true, "cifs": true, "smb3": true,
}

// mounts возвращает точки монтирования реальных ФС, по одной на устройство.
func (c *Collector) mounts() []string {
	f, err := os.Open(c.hostProc + "/mounts")
	if err != nil {
		return nil
	}
	defer f.Close()
	seenDev := map[string]bool{}
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fl := strings.Fields(sc.Text())
		if len(fl) < 3 || !realFS[fl[2]] {
			continue
		}
		dev, mnt := fl[0], unescapeMount(fl[1])
		if strings.HasPrefix(mnt, "/snap/") || strings.HasPrefix(mnt, "/var/lib/docker/") ||
			strings.HasPrefix(mnt, "/run/") || mnt == "/boot/efi" {
			continue
		}
		if seenDev[dev] {
			continue
		}
		seenDev[dev] = true
		out = append(out, mnt)
	}
	return out
}

func unescapeMount(s string) string {
	return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\134`, `\`).Replace(s)
}

// skipIface — виртуальные интерфейсы, которые только шумят на графиках.
func skipIface(name string) bool {
	for _, p := range []string{"lo", "veth", "docker", "br-", "virbr", "cali", "flannel", "cni", "vnet", "tap", "ifb", "dummy"} {
		if name == p || strings.HasPrefix(name, p) && p != "lo" {
			return true
		}
	}
	return false
}

func (c *Collector) readNet() map[string]netCounters {
	out := map[string]netCounters{}
	f, err := os.Open(c.hostProc + "/net/dev")
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		name, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if skipIface(name) {
			continue
		}
		fl := strings.Fields(rest)
		if len(fl) < 16 {
			continue
		}
		u := func(i int) uint64 { v, _ := strconv.ParseUint(fl[i], 10, 64); return v }
		out[name] = netCounters{rx: u(0), tx: u(8), errs: u(2) + u(10)}
	}
	return out
}

// Inventory собирает опись хоста.
func Inventory(version string) proto.Inventory {
	inv := proto.Inventory{OS: "linux", Arch: runtime.GOARCH, AgentVer: version, CPUCores: runtime.NumCPU()}
	inv.Hostname, _ = os.Hostname()
	if b, err := os.ReadFile("/etc/machine-id"); err == nil {
		inv.MachineID = strings.TrimSpace(string(b))
	}
	if b, err := os.ReadFile("/etc/os-release"); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(l, "PRETTY_NAME="); ok {
				inv.OSName = strings.Trim(v, `"`)
			}
		}
	}
	if b, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		inv.Kernel = strings.TrimSpace(string(b))
	}
	if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if k, v, ok := strings.Cut(l, ":"); ok && strings.TrimSpace(k) == "model name" {
				inv.CPUModel = strings.TrimSpace(v)
				break
			}
		}
	}
	c := &Collector{hostProc: "/proc"}
	inv.MemTotal = c.readMeminfo()["MemTotal"]
	if b, err := os.ReadFile("/proc/uptime"); err == nil {
		if f := strings.Fields(string(b)); len(f) > 0 {
			inv.BootTime = time.Now().Unix() - int64(atof(f[0]))
		}
	}
	if ifs, err := net.Interfaces(); err == nil {
		for _, i := range ifs {
			if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 || skipIface(i.Name) {
				continue
			}
			addrs, _ := i.Addrs()
			for _, a := range addrs {
				if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil {
					inv.IPs = append(inv.IPs, ipn.IP.String())
				}
			}
		}
	}
	return inv
}

func atof(s string) float64 { v, _ := strconv.ParseFloat(s, 64); return v }
func round2(v float64) float64 {
	if v < 0 {
		v = 0
	}
	return float64(int64(v*100+0.5)) / 100
}
