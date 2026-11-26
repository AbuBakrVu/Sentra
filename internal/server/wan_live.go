package server

import (
	"context"
	"net/http"
	"sync"
	"time"

	"sentra/internal/snmp"
)

const wanEvery = 5 * time.Second

type wanKey struct{ host, iface string }

type wanSample struct {
	deviceName string
	addr       string
	port       snmp.Iface
	uptime     uint32
	at         time.Time
	rx, tx     float64
	valid      bool
}

type wanPoller struct {
	mu      sync.RWMutex
	samples map[wanKey]wanSample
}

func (p *wanPoller) snapshot() map[wanKey]wanSample {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.samples // snapshots are immutable after publication
}

func (s *Server) pollWAN(ctx context.Context) {
	ticker := time.NewTicker(wanEvery)
	defer ticker.Stop()
	for {
		s.wanCycle(ctx, snmp.PollInterface)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

type wanInterfacePoll func(context.Context, string, snmp.Auth, int) (snmp.Iface, uint32, error)

func (s *Server) wanCycle(ctx context.Context, poll wanInterfacePoll) {
	channels := s.inetConfig().WANChannels
	p := &s.inet.wan
	previous := p.snapshot()
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	results := make([]wanSample, len(channels))
	var wg sync.WaitGroup
	for i, c := range channels {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = s.sampleWAN(ctx, c, previous[wanKey{c.Host, c.Interface}], poll)
		}()
	}
	wg.Wait()
	next := make(map[wanKey]wanSample, len(channels))
	for i, c := range channels {
		next[wanKey{c.Host, c.Interface}] = results[i]
	}
	p.mu.Lock()
	p.samples = next
	p.mu.Unlock()
}

func (s *Server) sampleWAN(ctx context.Context, c WANConfig, previous wanSample, poll wanInterfacePoll) wanSample {
	var sample wanSample
	host, err := s.st.Host(c.Host)
	if err != nil || host.Kind != kindSNMP {
		return sample
	}
	sample.deviceName = host.Name
	port := wanPort(host, c.Interface)
	index, _ := port["index"].(float64)
	name, _ := port["name"].(string)
	if index <= 0 {
		return sample
	}
	device, err := s.st.SNMPDevice(host.SourceID)
	if err != nil {
		return sample
	}
	iface, uptime, err := poll(ctx, device.Addr, device.Auth, int(index))
	if err != nil || iface.Name != name {
		return sample
	}
	sample.addr, sample.port, sample.uptime, sample.at = device.Addr, iface, uptime, time.Now()
	if dt := sample.at.Sub(previous.at); dt > 0 && dt <= 2*wanEvery && sample.addr == previous.addr &&
		iface.Index == previous.port.Index && uptime >= previous.uptime && iface.OperStatus == 1 && previous.port.OperStatus == 1 {
		sample.rx, sample.tx, sample.valid = snmpIfaceRates(previous.port, iface, dt.Seconds())
	}
	return sample
}

func (s *Server) getWAN(w http.ResponseWriter, r *http.Request, _ *User) {
	channels := s.inetConfig().WANChannels
	samples := s.inet.wan.snapshot()
	readings := make([]wanReading, 0, len(channels))
	now := time.Now()
	for _, c := range channels {
		sample := samples[wanKey{c.Host, c.Interface}]
		reading := wanReading{WANConfig: &c, DeviceName: sample.deviceName}
		if sample.valid && now.Sub(sample.at) < 3*wanEvery {
			reading.RX, reading.TX, reading.MeasuredAt = &sample.rx, &sample.tx, sample.at.Unix()
		}
		readings = append(readings, reading)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, readings)
}
