// Package netprobe проверяет, отвечает ли адрес: сначала ping (ICMP),
// а если ping недоступен или отфильтрован, то TCP-соединением.
package netprobe

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"strconv"
	"syscall"
	"time"
)

// Result — итог серии проверок одного адреса.
type Result struct {
	Sent  int     // сколько запросов отправили
	Recv  int     // на сколько пришёл ответ
	AvgMs float64 // средняя задержка по ответам
	MaxMs float64
	Via   string // icmp или tcp: чем в итоге получили ответ
}

// Loss — доля потерянных запросов в процентах.
func (r Result) Loss() float64 {
	if r.Sent == 0 {
		return 100
	}
	return 100 * float64(r.Sent-r.Recv) / float64(r.Sent)
}

func (r Result) Up() bool { return r.Recv > 0 }

const (
	pingCount    = 5
	pingInterval = time.Second
	pingTimeout  = 1500 * time.Millisecond
	tcpTimeout   = 2 * time.Second
)

// Prober сам выбирает способ: ping без прав root, ping через raw-сокет или TCP.
type Prober struct {
	open func() (net.PacketConn, bool, error) // соединение и признак raw-сокета
}

func New() *Prober {
	p := &Prober{}
	if c, err := openDgram(); err == nil {
		c.Close()
		p.open = func() (net.PacketConn, bool, error) { c, err := openDgram(); return c, false, err }
	} else if c, err := net.ListenPacket("ip4:icmp", "0.0.0.0"); err == nil {
		c.Close()
		p.open = func() (net.PacketConn, bool, error) {
			c, err := net.ListenPacket("ip4:icmp", "0.0.0.0")
			return c, true, err
		}
	}
	return p
}

// Method — основной способ проверки: icmp или tcp.
func (p *Prober) Method() string {
	if p.open != nil {
		return "icmp"
	}
	return "tcp"
}

// Probe проверяет адрес (IP или имя). ports — порты для TCP: соединение или
// явный отказ (RST) значат, что адрес жив. Если ping не получил ни одного
// ответа, перепроверяем по TCP: в некоторых сетях ICMP режут.
func (p *Prober) Probe(ctx context.Context, addr string, ports []int) Result {
	ip, err := resolve(ctx, addr)
	if err != nil {
		return Result{Sent: 1}
	}
	if p.open != nil {
		if r := p.ping(ctx, ip); r.Up() {
			return r
		}
		if ms, ok := tcpAlive(ctx, ip, ports); ok {
			return Result{Sent: 1, Recv: 1, AvgMs: ms, MaxMs: ms, Via: "tcp"}
		}
		return Result{Sent: pingCount, Via: "icmp"}
	}
	r := Result{Via: "tcp"}
	var sum float64
	for i := 0; i < 3 && ctx.Err() == nil; i++ {
		start := time.Now()
		r.Sent++
		if ms, ok := tcpAlive(ctx, ip, ports); ok {
			r.Recv++
			sum += ms
			if ms > r.MaxMs {
				r.MaxMs = ms
			}
		}
		sleep(ctx, pingInterval-time.Since(start))
	}
	if r.Recv > 0 {
		r.AvgMs = sum / float64(r.Recv)
	}
	return r
}

func resolve(ctx context.Context, addr string) (net.IP, error) {
	if ip := net.ParseIP(addr); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			return v4, nil
		}
		return nil, errors.New("только IPv4")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", addr)
	if err != nil || len(ips) == 0 {
		return nil, errors.New("имя не найдено")
	}
	return ips[0].To4(), nil
}

func sleep(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// ---------- ICMP ----------

func checksum(b []byte) uint16 {
	var s uint32
	for i := 0; i+1 < len(b); i += 2 {
		s += uint32(b[i])<<8 | uint32(b[i+1])
	}
	if len(b)%2 == 1 {
		s += uint32(b[len(b)-1]) << 8
	}
	for s>>16 != 0 {
		s = s&0xffff + s>>16
	}
	return ^uint16(s)
}

func echo(id, seq uint16, raw bool) []byte {
	b := make([]byte, 8+48)
	b[0] = 8 // echo request
	binary.BigEndian.PutUint16(b[4:], id)
	binary.BigEndian.PutUint16(b[6:], seq)
	binary.BigEndian.PutUint64(b[8:], uint64(time.Now().UnixNano()))
	if raw { // для dgram-сокета ядро само ставит id и контрольную сумму
		binary.BigEndian.PutUint16(b[2:], checksum(b))
	}
	return b
}

func (p *Prober) ping(ctx context.Context, ip net.IP) Result {
	r := Result{Via: "icmp"}
	c, raw, err := p.open()
	if err != nil {
		return r
	}
	defer c.Close()
	var dst net.Addr = &net.UDPAddr{IP: ip}
	if raw {
		dst = &net.IPAddr{IP: ip}
	}
	id := uint16(time.Now().UnixNano())
	buf := make([]byte, 1500)
	var sum float64
	for seq := uint16(1); seq <= pingCount && ctx.Err() == nil; seq++ {
		start := time.Now()
		r.Sent++
		if _, err := c.WriteTo(echo(id, seq, raw), dst); err != nil {
			sleep(ctx, pingInterval)
			continue
		}
		c.SetReadDeadline(start.Add(pingTimeout))
		for {
			n, from, err := c.ReadFrom(buf)
			if err != nil {
				break // таймаут: ответа нет
			}
			if n < 8 || buf[0] != 0 || binary.BigEndian.Uint16(buf[6:]) != seq || !sameIP(from, ip) {
				continue
			}
			if raw && binary.BigEndian.Uint16(buf[4:]) != id {
				continue
			}
			ms := float64(time.Since(start).Microseconds()) / 1000
			r.Recv++
			sum += ms
			if ms > r.MaxMs {
				r.MaxMs = ms
			}
			break
		}
		if seq < pingCount {
			sleep(ctx, pingInterval-time.Since(start))
		}
	}
	if r.Recv > 0 {
		r.AvgMs = sum / float64(r.Recv)
	}
	return r
}

func sameIP(a net.Addr, ip net.IP) bool {
	switch x := a.(type) {
	case *net.UDPAddr:
		return x.IP.Equal(ip)
	case *net.IPAddr:
		return x.IP.Equal(ip)
	}
	return false
}

// ---------- TCP ----------

// tcpAlive пробует порты по очереди. Отказ в соединении тоже ответ: адрес жив.
func tcpAlive(ctx context.Context, ip net.IP, ports []int) (float64, bool) {
	d := net.Dialer{Timeout: tcpTimeout}
	for _, port := range ports {
		start := time.Now()
		c, err := d.DialContext(ctx, "tcp4", net.JoinHostPort(ip.String(), strconv.Itoa(port)))
		ms := float64(time.Since(start).Microseconds()) / 1000
		if err == nil {
			c.Close()
			return ms, true
		}
		if errors.Is(err, syscall.ECONNREFUSED) {
			return ms, true
		}
	}
	return 0, false
}
