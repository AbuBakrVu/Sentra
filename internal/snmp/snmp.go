// Package snmp опрашивает сетевые устройства по SNMP v2c и v3: межсетевые
// экраны (Ideco NGFW, UserGate), маршрутизаторы, коммутаторы, NAS.
// Только стандартные MIB, поэтому подходит для любого производителя:
// SNMPv2-MIB (система), HOST-RESOURCES-MIB (CPU, память, диски),
// UCD-SNMP-MIB (память и нагрузка в net-snmp) и IF-MIB (интерфейсы).
package snmp

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gosnmp/gosnmp"
)

// Auth — параметры доступа. Для v3 пустые AuthProto и PrivProto значат
// «подобрать при подключении»: многие устройства (Ideco в их числе) не
// показывают в интерфейсе, какие алгоритмы используются.
type Auth struct {
	Version   string `json:"version"` // "2c" или "3"
	Community string `json:"-"`
	User      string `json:"user,omitempty"`
	AuthProto string `json:"auth_proto,omitempty"` // MD5, SHA, SHA-224, SHA-256, SHA-384, SHA-512
	AuthPass  string `json:"-"`
	PrivProto string `json:"priv_proto,omitempty"` // DES, AES, AES-192, AES-256, пусто — без шифрования
	PrivPass  string `json:"-"`
}

var (
	authProtos = map[string]gosnmp.SnmpV3AuthProtocol{
		"MD5": gosnmp.MD5, "SHA": gosnmp.SHA, "SHA-224": gosnmp.SHA224,
		"SHA-256": gosnmp.SHA256, "SHA-384": gosnmp.SHA384, "SHA-512": gosnmp.SHA512,
	}
	privProtos = map[string]gosnmp.SnmpV3PrivProtocol{
		"DES": gosnmp.DES, "AES": gosnmp.AES, "AES-192": gosnmp.AES192, "AES-256": gosnmp.AES256,
	}
	// порядок подбора: сначала то, что встречается чаще
	authOrder = []string{"SHA", "SHA-256", "MD5", "SHA-512", "SHA-224", "SHA-384"}
	privOrder = []string{"AES", "DES", "AES-256", "AES-192"}
)

// NormalizeAddr приводит адрес к виду host:port, порт по умолчанию 161.
func NormalizeAddr(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(strings.TrimPrefix(raw, "udp://"), "snmp://")
	if raw == "" {
		return "", errors.New("укажите IP-адрес устройства")
	}
	host, port, err := net.SplitHostPort(raw)
	if err != nil {
		host, port = strings.Trim(raw, "[]"), "161"
	}
	if host == "" || strings.ContainsAny(host, "/ ") {
		return "", errors.New("адрес устройства указан неверно")
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 {
		return "", errors.New("порт указан неверно")
	}
	return net.JoinHostPort(host, port), nil
}

func (a Auth) Validate() error {
	switch a.Version {
	case "2c":
		if a.Community == "" {
			return errors.New("укажите SNMP Community")
		}
	case "3":
		if a.User == "" {
			return errors.New("укажите имя пользователя SNMPv3")
		}
		if a.AuthPass != "" && len(a.AuthPass) < 8 {
			return errors.New("пароль SNMPv3 должен быть не короче 8 символов")
		}
		if a.PrivPass != "" && a.AuthPass == "" {
			return errors.New("ключ шифрования без пароля не используется: укажите пароль")
		}
		if a.PrivPass != "" && len(a.PrivPass) < 8 {
			return errors.New("ключ шифрования SNMPv3 должен быть не короче 8 символов")
		}
		if _, ok := authProtos[a.AuthProto]; a.AuthProto != "" && !ok {
			return fmt.Errorf("неизвестный алгоритм аутентификации %q", a.AuthProto)
		}
		if _, ok := privProtos[a.PrivProto]; a.PrivProto != "" && !ok {
			return fmt.Errorf("неизвестный алгоритм шифрования %q", a.PrivProto)
		}
	default:
		return errors.New("версия SNMP: 2c или 3")
	}
	return nil
}

func client(ctx context.Context, addr string, a Auth, timeout time.Duration, retries int) (*gosnmp.GoSNMP, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	p, _ := strconv.Atoi(port)
	g := &gosnmp.GoSNMP{
		Target: host, Port: uint16(p), Timeout: timeout, Retries: retries,
		Context: ctx, MaxOids: 30, MaxRepetitions: 25, ExponentialTimeout: false,
	}
	if a.Version == "2c" {
		g.Version = gosnmp.Version2c
		g.Community = a.Community
	} else {
		g.Version = gosnmp.Version3
		g.SecurityModel = gosnmp.UserSecurityModel
		usm := &gosnmp.UsmSecurityParameters{UserName: a.User}
		g.MsgFlags = gosnmp.NoAuthNoPriv
		if a.AuthPass != "" {
			g.MsgFlags = gosnmp.AuthNoPriv
			usm.AuthenticationProtocol = authProtos[a.AuthProto]
			usm.AuthenticationPassphrase = a.AuthPass
			if a.PrivPass != "" {
				g.MsgFlags = gosnmp.AuthPriv
				usm.PrivacyProtocol = privProtos[a.PrivProto]
				usm.PrivacyPassphrase = a.PrivPass
			}
		}
		g.SecurityParameters = usm
	}
	if err := g.Connect(); err != nil {
		return nil, friendly(err)
	}
	return g, nil
}

var errTimeout = errors.New("устройство не отвечает по SNMP. Проверьте, что SNMP на нём включён, " +
	"адрес коробки Sentra добавлен в доверенные адреса, а UDP-порт 161 открыт")

func friendly(err error) error {
	if err == nil {
		return nil
	}
	s := err.Error()
	switch {
	case errors.Is(err, gosnmp.ErrUnknownUsername):
		return errors.New("устройство не знает такого пользователя SNMPv3: проверьте имя пользователя")
	case errors.Is(err, gosnmp.ErrWrongDigest), strings.Contains(s, "not authentic"):
		// ответ на неверный пароль или незнакомого пользователя устройство не подписывает
		return errors.New("неверное имя пользователя или пароль SNMPv3")
	case errors.Is(err, gosnmp.ErrDecryption):
		return errors.New("неверный ключ шифрования SNMPv3")
	case errors.Is(err, gosnmp.ErrUnknownSecurityLevel):
		return errors.New("уровень защиты не совпадает с настройками устройства: проверьте, нужен ли ключ шифрования")
	case strings.Contains(s, "timeout") || strings.Contains(s, "deadline exceeded") || strings.Contains(s, "i/o timeout"):
		return errTimeout
	case strings.Contains(s, "connection refused"):
		return errors.New("устройство отклонило запрос: SNMP на нём выключен или слушает другой порт")
	case strings.Contains(s, "no such host"):
		return errors.New("имя устройства не найдено в DNS, укажите IP-адрес")
	}
	return err
}

// System — то, что устройство говорит о себе.
type System struct {
	Name, Descr, Location, Contact, ObjectID string
	Uptime                                   float64 // секунды
}

const (
	oidSysDescr    = ".1.3.6.1.2.1.1.1.0"
	oidSysObjectID = ".1.3.6.1.2.1.1.2.0"
	oidSysUpTime   = ".1.3.6.1.2.1.1.3.0"
	oidSysContact  = ".1.3.6.1.2.1.1.4.0"
	oidSysName     = ".1.3.6.1.2.1.1.5.0"
	oidSysLocation = ".1.3.6.1.2.1.1.6.0"
	oidHrUptime    = ".1.3.6.1.2.1.25.1.1.0"

	// UCD-SNMP-MIB (net-snmp), значения в килобайтах
	oidMemTotal  = ".1.3.6.1.4.1.2021.4.5.0"
	oidMemAvail  = ".1.3.6.1.4.1.2021.4.6.0"
	oidMemBuffer = ".1.3.6.1.4.1.2021.4.14.0"
	oidMemCached = ".1.3.6.1.4.1.2021.4.15.0"
	oidLoad1     = ".1.3.6.1.4.1.2021.10.1.3.1"
	oidSsCPURaw  = ".1.3.6.1.4.1.2021.11" // ssCpuRaw*: 50..64

	oidHrProcLoad    = ".1.3.6.1.2.1.25.3.3.1.2"
	oidHrStorage     = ".1.3.6.1.2.1.25.2.3.1"
	hrStorageRAM     = ".1.3.6.1.2.1.25.2.1.2"
	hrStorageFixed   = ".1.3.6.1.2.1.25.2.1.4"
	hrStorageNetDisk = ".1.3.6.1.2.1.25.2.1.10"

	oidIfTable  = ".1.3.6.1.2.1.2.2.1"
	oidIfXTable = ".1.3.6.1.2.1.31.1.1.1"
)

func get(g *gosnmp.GoSNMP, oids ...string) (map[string]gosnmp.SnmpPDU, error) {
	out := map[string]gosnmp.SnmpPDU{}
	r, err := g.Get(oids)
	if err != nil {
		return nil, friendly(err)
	}
	if r.Error == gosnmp.AuthorizationError {
		return nil, errors.New("устройство отказало в доступе: у пользователя нет прав на чтение " +
			"или на устройстве требуется пароль и ключ шифрования, а они не указаны")
	}
	if r.Error != gosnmp.NoError {
		return nil, fmt.Errorf("устройство вернуло ошибку SNMP: %v", r.Error)
	}
	for _, v := range r.Variables {
		if v.Type == gosnmp.NoSuchObject || v.Type == gosnmp.NoSuchInstance || v.Type == gosnmp.EndOfMibView || v.Type == gosnmp.Null {
			continue
		}
		out[v.Name] = v
	}
	return out, nil
}

func str(v gosnmp.SnmpPDU) string {
	switch x := v.Value.(type) {
	case []byte:
		return strings.TrimRight(strings.ToValidUTF8(string(x), "?"), "\x00")
	case string:
		return x
	}
	return fmt.Sprint(v.Value)
}

func num(v gosnmp.SnmpPDU) float64 {
	if s, ok := v.Value.([]byte); ok { // laLoad — строка «0.15»
		f, _ := strconv.ParseFloat(strings.TrimSpace(string(s)), 64)
		return f
	}
	b := gosnmp.ToBigInt(v.Value)
	f, _ := new(big.Float).SetInt(b).Float64()
	return f
}

func readSystem(g *gosnmp.GoSNMP) (System, error) {
	m, err := get(g, oidSysDescr, oidSysObjectID, oidSysUpTime, oidSysContact, oidSysName, oidSysLocation, oidHrUptime)
	if err != nil {
		return System{}, err
	}
	if len(m) == 0 {
		return System{}, errors.New("устройство ответило, но не отдаёт даже системную информацию (SNMPv2-MIB): проверьте права на просмотр")
	}
	s := System{Name: str(m[oidSysName]), Descr: str(m[oidSysDescr]), Location: str(m[oidSysLocation]),
		Contact: str(m[oidSysContact]), ObjectID: str(m[oidSysObjectID])}
	if v, ok := m[oidHrUptime]; ok {
		s.Uptime = num(v) / 100 // время работы системы, не агента
	} else if v, ok := m[oidSysUpTime]; ok {
		s.Uptime = num(v) / 100
	}
	return s, nil
}

// Probe подключается, для v3 при необходимости подбирает алгоритмы и
// возвращает рабочие параметры вместе с данными системы.
func Probe(ctx context.Context, addr string, a Auth) (Auth, System, error) {
	// при подборе пробуем быстро и без повторов: вариантов бывает до 24
	quick := false
	try := func(a Auth) (System, error) {
		timeout, retries := 2*time.Second, 1
		if quick {
			timeout, retries = 1500*time.Millisecond, 0
		}
		g, err := client(ctx, addr, a, timeout, retries)
		if err != nil {
			return System{}, err
		}
		defer g.Conn.Close()
		return readSystem(g)
	}
	if a.Version != "3" || a.AuthPass == "" || (a.AuthProto != "" && (a.PrivPass == "" || a.PrivProto != "")) {
		sys, err := try(a)
		return a, sys, err
	}
	// подбор: сначала аутентификация, затем шифрование
	quick = true
	var lastErr error
	auths := authOrder
	if a.AuthProto != "" {
		auths = []string{a.AuthProto}
	}
	for _, ap := range auths {
		b := a
		b.AuthProto = ap
		if b.PrivPass == "" {
			sys, err := try(b)
			if err == nil {
				return b, sys, nil
			}
			if errors.Is(err, errTimeout) || ctx.Err() != nil {
				return a, System{}, err
			}
			lastErr = err
			continue
		}
		for _, pp := range privOrder {
			b.PrivProto = pp
			sys, err := try(b)
			if err == nil {
				return b, sys, nil
			}
			lastErr = err
			if ctx.Err() != nil {
				return a, System{}, err
			}
		}
	}
	if lastErr == nil {
		lastErr = errors.New("не удалось подобрать параметры SNMPv3")
	}
	if errors.Is(lastErr, errTimeout) {
		return a, System{}, errors.New("устройство не отвечает по SNMPv3: проверьте пароль и ключ шифрования, " +
			"а также что адрес коробки Sentra добавлен в доверенные адреса")
	}
	return a, System{}, lastErr
}

// ---------- опрос ----------

type Disk struct {
	Mount       string
	Total, Used float64 // байты
}

type Iface struct {
	Index       int
	Name, Alias string
	Type        int
	Up          bool    // ifOperStatus == up
	AdminUp     bool    // ifAdminStatus == up
	Speed       float64 // бит/с
	In, Out     uint64  // счётчики байт
	HC          bool    // 64-битные счётчики
}

// CPURaw — суммы счётчиков ssCpuRaw* для расчёта загрузки по разнице.
type CPURaw struct{ Busy, Total float64 }

type Snapshot struct {
	System
	CPU               float64 // %, -1 — нет данных
	CPURaw            *CPURaw
	MemTotal, MemUsed float64 // байты, 0 — нет данных
	Load1             float64 // -1 — нет данных
	Disks             []Disk
	Ifaces            []Iface
}

func walk(g *gosnmp.GoSNMP, root string) ([]gosnmp.SnmpPDU, error) {
	var r []gosnmp.SnmpPDU
	var err error
	if g.Version == gosnmp.Version1 {
		r, err = g.WalkAll(root)
	} else {
		r, err = g.BulkWalkAll(root)
	}
	if err != nil {
		return nil, friendly(err)
	}
	return r, nil
}

// table раскладывает обход таблицы: колонка → индекс → значение.
func table(rows []gosnmp.SnmpPDU, root string) map[int]map[string]gosnmp.SnmpPDU {
	out := map[int]map[string]gosnmp.SnmpPDU{}
	p := root + "."
	for _, v := range rows {
		if !strings.HasPrefix(v.Name, p) {
			continue
		}
		rest := strings.TrimPrefix(v.Name, p)
		col, idx, ok := strings.Cut(rest, ".")
		if !ok {
			continue
		}
		c, err := strconv.Atoi(col)
		if err != nil {
			continue
		}
		if out[c] == nil {
			out[c] = map[string]gosnmp.SnmpPDU{}
		}
		out[c][idx] = v
	}
	return out
}

// Poll снимает одно состояние устройства. Отсутствие отдельных веток MIB —
// не ошибка: у коммутатора нет дисков, у MikroTik нет UCD-SNMP-MIB.
func Poll(ctx context.Context, addr string, a Auth) (*Snapshot, error) {
	g, err := client(ctx, addr, a, 3*time.Second, 1)
	if err != nil {
		return nil, err
	}
	defer g.Conn.Close()
	sys, err := readSystem(g)
	if err != nil {
		return nil, err
	}
	s := &Snapshot{System: sys, CPU: -1, Load1: -1}

	// процессор: средняя загрузка ядер за минуту (HOST-RESOURCES)
	if rows, err := walk(g, oidHrProcLoad); err == nil && len(rows) > 0 {
		var sum float64
		for _, v := range rows {
			sum += num(v)
		}
		s.CPU = sum / float64(len(rows))
	} else if rows, err := walk(g, oidSsCPURaw); err == nil {
		// net-snmp без hrProcessorLoad: сырые счётчики, загрузку считаем по разнице
		var raw CPURaw
		for _, v := range rows {
			var n int
			if _, err := fmt.Sscanf(strings.TrimPrefix(v.Name, oidSsCPURaw+"."), "%d.0", &n); err != nil {
				continue
			}
			switch n {
			case 50, 51, 52, 56, 61, 64: // user, nice, system, interrupt, softirq, steal
				raw.Busy += num(v)
				raw.Total += num(v)
			case 53, 54: // idle, wait
				raw.Total += num(v)
			}
		}
		if raw.Total > 0 {
			s.CPURaw = &raw
		}
	}

	// память и нагрузка: UCD-SNMP-MIB точнее, чем hrStorage (там кеш считается занятым)
	if m, err := get(g, oidMemTotal, oidMemAvail, oidMemBuffer, oidMemCached, oidLoad1); err == nil {
		if t, ok := m[oidMemTotal]; ok && num(t) > 0 {
			total := num(t) * 1024
			free := (num(m[oidMemAvail]) + num(m[oidMemBuffer]) + num(m[oidMemCached])) * 1024
			s.MemTotal = total
			s.MemUsed = max(0, total-free)
		}
		if v, ok := m[oidLoad1]; ok {
			s.Load1 = num(v)
		}
	}

	if rows, err := walk(g, oidHrStorage); err == nil {
		t := table(rows, oidHrStorage)
		for idx, typ := range t[2] {
			tp := str(typ)
			if !strings.HasPrefix(tp, ".") {
				tp = "." + tp
			}
			units := num(t[4][idx])
			size := num(t[5][idx]) * units
			used := num(t[6][idx]) * units
			descr := str(t[3][idx])
			switch tp {
			case hrStorageRAM:
				if s.MemTotal == 0 && size > 0 {
					s.MemTotal, s.MemUsed = size, used
				}
			case hrStorageFixed, hrStorageNetDisk:
				if size > 0 {
					s.Disks = append(s.Disks, Disk{Mount: descr, Total: size, Used: used})
				}
			}
		}
		s.Disks = usableDisks(s.Disks)
	}

	ifs, err := walk(g, oidIfTable)
	if err != nil {
		return nil, err
	}
	xs, _ := walk(g, oidIfXTable) // ifXTable есть не у всех
	it, xt := table(ifs, oidIfTable), table(xs, oidIfXTable)
	for idxS, d := range it[2] { // ifDescr
		idx, _ := strconv.Atoi(idxS)
		f := Iface{Index: idx, Name: str(d), Type: int(num(it[3][idxS])),
			Up: num(it[8][idxS]) == 1, AdminUp: num(it[7][idxS]) == 1,
			Speed: num(it[5][idxS]),
			In:    uint64(num(it[10][idxS])), Out: uint64(num(it[16][idxS]))}
		if n, ok := xt[1][idxS]; ok && str(n) != "" {
			f.Name = str(n)
		}
		if al, ok := xt[18][idxS]; ok {
			f.Alias = str(al)
		}
		if hs, ok := xt[15][idxS]; ok && num(hs) > 0 {
			f.Speed = num(hs) * 1e6 // ifHighSpeed в Мбит/с
		}
		if in, ok := xt[6][idxS]; ok {
			if out, ok := xt[10][idxS]; ok {
				f.In, f.Out, f.HC = gosnmp.ToBigInt(in.Value).Uint64(), gosnmp.ToBigInt(out.Value).Uint64(), true
			}
		}
		if f.Type == 24 { // softwareLoopback
			continue
		}
		s.Ifaces = append(s.Ifaces, f)
	}
	sort.Slice(s.Ifaces, func(i, j int) bool { return s.Ifaces[i].Index < s.Ifaces[j].Index })
	return s, nil
}

// usableDisks убирает служебные файловые системы, как агент Sentra.
func usableDisks(in []Disk) []Disk {
	var out []Disk
	seen := map[string]bool{}
	for _, d := range in {
		m := d.Mount
		if d.Total < 64<<20 || seen[m] {
			continue
		}
		if m == "/boot" || m == "/boot/efi" || strings.HasPrefix(m, "/run") || strings.HasPrefix(m, "/dev") ||
			strings.HasPrefix(m, "/sys") || strings.HasPrefix(m, "/proc") || strings.HasPrefix(m, "/snap/") ||
			strings.HasPrefix(m, "/var/lib/docker/") {
			continue
		}
		// Windows: «C:\ Label:System  Serial Number 1234abcd» → «C:»
		if len(m) >= 2 && m[1] == ':' {
			m = strings.ToUpper(m[:2])
		}
		seen[m] = true
		d.Mount = m
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Mount < out[j].Mount })
	return out
}
