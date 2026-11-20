// Package vmware опрашивает VMware vSphere (vCenter или отдельный ESXi) через
// vSphere API: хосты ESXi, виртуальные машины и хранилища (datastore).
// Нужен пользователь с ролью Read-only на корне с наследованием.
package vmware

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/vmware/govmomi/fault"
	"github.com/vmware/govmomi/session"
	"github.com/vmware/govmomi/view"
	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/soap"
	"github.com/vmware/govmomi/vim25/types"
)

var (
	ErrFingerprint = errors.New("сертификат VMware сменился: отпечаток не совпадает с сохранённым")
	ErrLogin       = errors.New("VMware не принял логин или пароль")
)

// NormalizeURL приводит адрес к виду https://host[:port]. Путь /sdk клиент
// добавляет сам, поэтому вставленный из браузера адрес тоже подходит.
func NormalizeURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("укажите адрес vCenter или ESXi")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", errors.New("адрес VMware указан неверно")
	}
	if u.Scheme != "https" {
		return "", errors.New("VMware работает только по https")
	}
	host := u.Host
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(strings.Trim(host, "[]"), "443")
	}
	if strings.HasSuffix(host, ":443") {
		host = strings.TrimSuffix(host, ":443")
	}
	return "https://" + host, nil
}

// Fingerprint — SHA-256 сертификата в виде AA:BB:..., как показывает vSphere Client.
func Fingerprint(cert *x509.Certificate) string {
	h := sha256.Sum256(cert.Raw)
	parts := make([]string, len(h))
	for i, b := range h {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, ":")
}

func hostPort(base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	if _, _, err := net.SplitHostPort(u.Host); err != nil {
		return net.JoinHostPort(strings.Trim(u.Host, "[]"), "443"), nil
	}
	return u.Host, nil
}

// FetchFingerprint подключается и возвращает отпечаток сертификата сервера.
func FetchFingerprint(ctx context.Context, base string) (string, error) {
	addr, err := hostPort(base)
	if err != nil {
		return "", err
	}
	d := &tls.Dialer{NetDialer: &net.Dialer{Timeout: 5 * time.Second}, Config: &tls.Config{InsecureSkipVerify: true}}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return "", friendlyNetErr(err)
	}
	defer conn.Close()
	st := conn.(*tls.Conn).ConnectionState()
	if len(st.PeerCertificates) == 0 {
		return "", errors.New("VMware не прислал сертификат")
	}
	return Fingerprint(st.PeerCertificates[0]), nil
}

func friendlyNetErr(err error) error {
	var ne net.Error
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrFingerprint) || strings.Contains(err.Error(), ErrFingerprint.Error()):
		return ErrFingerprint
	case errors.As(err, &ne) && ne.Timeout(), errors.Is(err, context.DeadlineExceeded):
		return errors.New("VMware не отвечает: проверьте адрес и что порт 443 доступен с коробки")
	case strings.Contains(err.Error(), "connection refused"):
		return errors.New("соединение отклонено: на этом адресе не работает vCenter или ESXi (порт 443)")
	case strings.Contains(err.Error(), "no such host"):
		return errors.New("имя VMware не найдено в DNS, укажите IP-адрес")
	}
	return err
}

// Client держит одну сессию vSphere и переиспользует её между опросами:
// вход на каждый опрос плодил бы сессии в vCenter.
type Client struct {
	base, user, pass, pin string

	mu sync.Mutex
	vc *vim25.Client
}

// New создаёт клиент. Если pin пустой, сертификат не проверяется: так делаем
// только при первом подключении, чтобы узнать и запомнить отпечаток.
func New(base, user, pass, pin string) *Client {
	return &Client{base: strings.TrimRight(base, "/"), user: strings.TrimSpace(user), pass: pass,
		pin: strings.ToUpper(strings.TrimSpace(pin))}
}

func (c *Client) login(ctx context.Context) (*vim25.Client, error) {
	u, err := url.Parse(c.base + "/sdk")
	if err != nil {
		return nil, err
	}
	sc := soap.NewClient(u, true) // проверяем сами: по отпечатку
	t := sc.DefaultTransport()
	t.Proxy = nil // VMware в локальной сети, прокси не нужен
	t.DialContext = (&net.Dialer{Timeout: 5 * time.Second}).DialContext
	t.TLSHandshakeTimeout = 5 * time.Second
	pin := c.pin
	t.TLSClientConfig.VerifyPeerCertificate = func(raw [][]byte, _ [][]*x509.Certificate) error {
		if pin == "" {
			return nil
		}
		if len(raw) == 0 {
			return errors.New("VMware не прислал сертификат")
		}
		cert, err := x509.ParseCertificate(raw[0])
		if err != nil {
			return err
		}
		if Fingerprint(cert) != pin {
			return ErrFingerprint
		}
		return nil
	}
	sc.Timeout = 20 * time.Second
	vc, err := vim25.NewClient(ctx, sc)
	if err != nil {
		return nil, friendlyNetErr(err)
	}
	if err := session.NewManager(vc).Login(ctx, url.UserPassword(c.user, c.pass)); err != nil {
		if fault.Is(err, &types.InvalidLogin{}) {
			return nil, ErrLogin
		}
		return nil, friendlyNetErr(err)
	}
	return vc, nil
}

// do выполняет запрос в сессии; если сессия истекла, входит заново один раз.
func (c *Client) do(ctx context.Context, fn func(*vim25.Client) error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for try := 0; try < 2; try++ {
		if c.vc == nil {
			vc, err := c.login(ctx)
			if err != nil {
				return err
			}
			c.vc = vc
		}
		err := fn(c.vc)
		if err == nil {
			return nil
		}
		if fault.Is(err, &types.NotAuthenticated{}) && try == 0 {
			c.vc = nil // сессию закрыли (перезагрузка vCenter, таймаут): входим снова
			continue
		}
		if !soap.IsSoapFault(err) && !soap.IsVimFault(err) {
			c.vc = nil // сетевая ошибка: в следующий раз начнём с чистого соединения
		}
		return friendlyNetErr(err)
	}
	return errors.New("VMware закрывает сессию сразу после входа")
}

// Logout закрывает сессию, например когда подключение удалили.
func (c *Client) Logout(ctx context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.vc != nil {
		session.NewManager(c.vc).Logout(ctx)
		c.vc = nil
	}
}

// About — что это за сервер: vCenter (ApiType VirtualCenter) или ESXi (HostAgent).
type About struct {
	Name, FullName, Version, Build, APIType string
}

func (a About) IsVCenter() bool { return a.APIType == "VirtualCenter" }

func (c *Client) About(ctx context.Context) (About, error) {
	var a About
	err := c.do(ctx, func(vc *vim25.Client) error {
		ab := vc.ServiceContent.About
		a = About{Name: ab.Name, FullName: ab.FullName, Version: ab.Version, Build: ab.Build, APIType: ab.ApiType}
		// ServiceContent получен до входа: проверим, что сессия действительно работает
		_, err := session.NewManager(vc).UserSession(ctx)
		return err
	})
	return a, err
}

// ---------- инвентарь ----------

type Host struct {
	Ref, Name, Cluster string
	Connected          bool   // runtime.connectionState == connected
	Connection         string // connected, disconnected, notResponding
	Power              string // poweredOn, standBy, poweredOff, unknown
	Maintenance        bool
	CPUMhzPerCore      float64
	CPUMhzTotal        float64
	CPUMhzUsed         float64
	Cores, Threads     int
	MemTotal, MemUsed  float64 // байты
	Uptime             float64 // секунды
	Product            string  // VMware ESXi 8.0.3 build-24022510
	Version            string
	CPUModel           string
	Vendor, Model      string
	Datastores         []string // ref
}

type Disk struct {
	Path           string
	Capacity, Free float64
}

type VM struct {
	Ref, Name, HostRef string
	Template           bool
	Power              string // poweredOn, poweredOff, suspended
	Connection         string // connected, disconnected, orphaned, inaccessible, invalid
	NumCPU             int
	MemTotal           float64 // байты
	CPUMhzUsed         float64
	MemActive          float64 // байты, активная память по оценке ESXi
	MemConsumed        float64 // байты, сколько памяти хоста занимает машина
	Uptime             float64
	GuestOS            string
	IP, GuestHost      string
	Tools              string // guestToolsRunning, guestToolsNotRunning, guestToolsExecutingScripts
	ToolsVersion       string // guestToolsCurrent, guestToolsNotInstalled, guestToolsNeedUpgrade…
	StorageUsed        float64
	Disks              []Disk // из VMware Tools
}

type Datastore struct {
	Ref, Name, Type string
	Capacity, Free  float64
	Accessible      bool
	Shared          bool
}

type Inventory struct {
	Hosts      []Host
	VMs        []VM
	Datastores map[string]Datastore // ref → хранилище
}

// Inventory одним заходом забирает хосты, машины и хранилища. Только быстрые
// свойства (quickStats): счётчики производительности здесь не нужны.
func (c *Client) Inventory(ctx context.Context) (*Inventory, error) {
	out := &Inventory{Datastores: map[string]Datastore{}}
	err := c.do(ctx, func(vc *vim25.Client) error {
		m := view.NewManager(vc)
		v, err := m.CreateContainerView(ctx, vc.ServiceContent.RootFolder,
			[]string{"HostSystem", "VirtualMachine", "Datastore", "ComputeResource"}, true)
		if err != nil {
			return err
		}
		defer v.Destroy(context.WithoutCancel(ctx))

		var crs []mo.ComputeResource
		if err := v.Retrieve(ctx, []string{"ComputeResource"}, []string{"name"}, &crs); err != nil {
			return err
		}
		clusters := map[string]string{}
		for _, cr := range crs {
			if cr.Self.Type == "ClusterComputeResource" {
				clusters[cr.Self.Value] = cr.Name
			}
		}

		var hs []mo.HostSystem
		if err := v.Retrieve(ctx, []string{"HostSystem"}, []string{"name", "parent", "datastore",
			"summary.hardware", "summary.quickStats", "summary.config.product",
			"runtime.connectionState", "runtime.powerState", "runtime.inMaintenanceMode"}, &hs); err != nil {
			return err
		}
		for _, h := range hs {
			x := Host{Ref: h.Self.Value, Name: h.Name,
				Connection: string(h.Runtime.ConnectionState), Power: string(h.Runtime.PowerState),
				Maintenance: h.Runtime.InMaintenanceMode}
			x.Connected = x.Connection == string(types.HostSystemConnectionStateConnected)
			if h.Parent != nil {
				x.Cluster = clusters[h.Parent.Value]
			}
			if hw := h.Summary.Hardware; hw != nil {
				x.CPUMhzPerCore = float64(hw.CpuMhz)
				x.Cores, x.Threads = int(hw.NumCpuCores), int(hw.NumCpuThreads)
				x.CPUMhzTotal = float64(hw.CpuMhz) * float64(hw.NumCpuCores)
				x.MemTotal = float64(hw.MemorySize)
				x.CPUModel = strings.Join(strings.Fields(hw.CpuModel), " ")
				x.Vendor, x.Model = hw.Vendor, hw.Model
			}
			q := h.Summary.QuickStats
			x.CPUMhzUsed = float64(q.OverallCpuUsage)
			x.MemUsed = float64(q.OverallMemoryUsage) * (1 << 20)
			x.Uptime = float64(q.Uptime)
			if p := h.Summary.Config.Product; p != nil {
				x.Product = p.FullName
				x.Version = p.Version
			}
			for _, d := range h.Datastore {
				x.Datastores = append(x.Datastores, d.Value)
			}
			out.Hosts = append(out.Hosts, x)
		}

		var ds []mo.Datastore
		if err := v.Retrieve(ctx, []string{"Datastore"}, []string{"summary"}, &ds); err != nil {
			return err
		}
		for _, d := range ds {
			s := d.Summary
			x := Datastore{Ref: d.Self.Value, Name: s.Name, Type: s.Type, Capacity: float64(s.Capacity),
				Free: float64(s.FreeSpace), Accessible: s.Accessible}
			if s.MultipleHostAccess != nil {
				x.Shared = *s.MultipleHostAccess
			}
			out.Datastores[x.Ref] = x
		}

		var vms []mo.VirtualMachine
		if err := v.Retrieve(ctx, []string{"VirtualMachine"}, []string{"name",
			"summary.config", "summary.runtime", "summary.quickStats", "summary.guest", "summary.storage",
			"guest.disk"}, &vms); err != nil {
			return err
		}
		for _, m := range vms {
			s := m.Summary
			x := VM{Ref: m.Self.Value, Name: m.Name, Template: s.Config.Template,
				Power: string(s.Runtime.PowerState), Connection: string(s.Runtime.ConnectionState),
				NumCPU: int(s.Config.NumCpu), MemTotal: float64(s.Config.MemorySizeMB) * (1 << 20),
				GuestOS: s.Config.GuestFullName}
			if s.Runtime.Host != nil {
				x.HostRef = s.Runtime.Host.Value
			}
			q := s.QuickStats
			x.CPUMhzUsed = float64(q.OverallCpuUsage)
			x.MemActive = float64(q.GuestMemoryUsage) * (1 << 20)
			x.MemConsumed = float64(q.HostMemoryUsage) * (1 << 20)
			x.Uptime = float64(q.UptimeSeconds)
			if g := s.Guest; g != nil {
				x.Tools, x.ToolsVersion = g.ToolsRunningStatus, g.ToolsVersionStatus2
				x.IP, x.GuestHost = g.IpAddress, g.HostName
				if g.GuestFullName != "" {
					x.GuestOS = g.GuestFullName
				}
			}
			if st := s.Storage; st != nil {
				x.StorageUsed = float64(st.Committed)
			}
			if m.Guest != nil {
				for _, d := range m.Guest.Disk {
					if d.Capacity > 0 {
						x.Disks = append(x.Disks, Disk{Path: d.DiskPath, Capacity: float64(d.Capacity), Free: float64(d.FreeSpace)})
					}
				}
			}
			out.VMs = append(out.VMs, x)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// UsableDisks оставляет настоящие разделы гостя, как и для Proxmox: без
// служебных точек монтирования, крошечных загрузочных разделов и повторов.
// Windows-диски приводит к виду «C:».
func UsableDisks(in []Disk) []Disk {
	var out []Disk
	seen := map[string]bool{}
	for _, d := range in {
		p := d.Path
		if d.Capacity < 64<<20 || p == "" {
			continue
		}
		if !strings.HasPrefix(p, "/") && !(len(p) >= 2 && p[1] == ':') {
			continue // тома без буквы («System Reserved»)
		}
		if p == "/boot" || p == "/boot/efi" || strings.HasPrefix(p, "/snap/") ||
			strings.HasPrefix(p, "/run/") || strings.HasPrefix(p, "/var/lib/docker/") {
			continue
		}
		if len(p) >= 2 && p[1] == ':' {
			p = strings.TrimRight(p, `\`)
			if len(p) == 2 {
				p = strings.ToUpper(p)
			}
		}
		if seen[p] {
			continue
		}
		seen[p] = true
		d.Path = p
		out = append(out, d)
	}
	return out
}
