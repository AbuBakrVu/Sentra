package server

import (
	"context"
	"errors"
	"log"
	"os"
	"sync"
	"time"

	"sentra/internal/proto"
	"sentra/internal/proxmox"
)

// Диски внутри ВМ Proxmox: спрашиваем у гостевого агента QEMU. Отдельно от
// основного опроса и реже: заполнение диска быстро не меняется, а запрос
// нужен на каждую машину.

const (
	guestEvery     = 5 * time.Minute  // машина с агентом
	guestNoAgent   = 10 * time.Minute // агента нет: вдруг его поставят
	guestForbidden = 30 * time.Minute // у токена нет права: спрашивать бесполезно, пока не выдадут
	guestError     = 10 * time.Minute
	guestStale     = 20 * time.Minute // старше этого данные о дисках не показываем
	guestParallel  = 6
	guestTimeout   = 8 * time.Second
)

// Состояние гостевого агента машины, оно же подсказка в интерфейсе.
const (
	guestPending = "pending"   // ещё не спрашивали
	guestOK      = "ok"        // агент ответил
	guestMissing = "noagent"   // агент не включён или не запущен
	guestNoRight = "forbidden" // у токена нет права
	guestFailed  = "error"
)

type guestState struct {
	status string
	fs     []proxmox.FS
	at     time.Time // когда получили fs
	next   time.Time // когда спрашивать снова
	err    string
}

type guestJob struct {
	hostID string // ключ: подключение и id ресурса, например «src1|qemu/105»
	node   string
	vmid   int
}

type guestPoller struct {
	mu        sync.Mutex
	vms       map[string]*guestState // ключ машины → состояние
	forbidden map[string]time.Time   // подключение → до какого времени не спрашивать
	busy      map[string]bool        // подключение → обход уже идёт
}

func newGuestPoller() *guestPoller {
	return &guestPoller{vms: map[string]*guestState{}, forbidden: map[string]time.Time{}, busy: map[string]bool{}}
}

// state возвращает состояние машины для подсказки и диски, если они свежие.
func (g *guestPoller) state(hostID string, now time.Time) (string, []proxmox.FS) {
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.vms[hostID]
	if st == nil {
		return guestPending, nil
	}
	if st.status == guestOK && now.Sub(st.at) < guestStale {
		return guestOK, st.fs
	}
	if st.status == guestOK {
		return guestPending, nil
	}
	return st.status, nil
}

// due отбирает машины, которые пора спросить, и помечает обход подключения начатым.
func (g *guestPoller) due(srcID string, jobs []guestJob, now time.Time) []guestJob {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.busy[srcID] {
		return nil
	}
	if until, ok := g.forbidden[srcID]; ok && now.Before(until) {
		for _, j := range jobs {
			if st := g.vms[j.hostID]; st == nil || st.status == guestPending {
				g.vms[j.hostID] = &guestState{status: guestNoRight, next: until}
			}
		}
		return nil
	}
	var out []guestJob
	for _, j := range jobs {
		if st := g.vms[j.hostID]; st == nil || !now.Before(st.next) {
			out = append(out, j)
		}
	}
	if len(out) > 0 {
		g.busy[srcID] = true
	}
	return out
}

func (g *guestPoller) set(hostID string, st *guestState) {
	g.mu.Lock()
	if old := g.vms[hostID]; old != nil && st.status != guestOK && old.status == guestOK {
		st.fs, st.at = old.fs, old.at // временный сбой: старые данные дотянут до guestStale
	}
	g.vms[hostID] = st
	g.mu.Unlock()
}

// pollGuests обходит машины подключения в фоне и не задерживает основной опрос.
func (s *Server) pollGuests(ctx context.Context, src *PVESource, cl *proxmox.Client, jobs []guestJob) {
	g := s.guest
	jobs = g.due(src.ID, jobs, time.Now())
	if len(jobs) == 0 {
		return
	}
	// основной опрос закончится раньше: обход живёт со своим сроком
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Minute)
	go func() {
		defer cancel()
		defer func() { g.mu.Lock(); g.busy[src.ID] = false; g.mu.Unlock() }()
		started := time.Now()
		sem := make(chan struct{}, guestParallel)
		var wg sync.WaitGroup
		var once sync.Once
		var mu sync.Mutex
		counts := map[string]int{}
		stop := make(chan struct{})
		for _, j := range jobs {
			select {
			case <-stop:
			case <-ctx.Done():
			case sem <- struct{}{}:
				wg.Add(1)
				go func(j guestJob) {
					defer func() { <-sem; wg.Done() }()
					cctx, cancel := context.WithTimeout(ctx, guestTimeout)
					fs, err := cl.AgentFSInfo(cctx, j.node, j.vmid)
					cancel()
					now := time.Now()
					st := &guestState{status: guestOK, fs: fs, at: now, next: now.Add(guestEvery)}
					switch {
					case err == nil:
					case errors.Is(err, proxmox.ErrNoAgent):
						st = &guestState{status: guestMissing, next: now.Add(guestNoAgent)}
					case errors.Is(err, proxmox.ErrAgentForbidden):
						st = &guestState{status: guestNoRight, next: now.Add(guestForbidden)}
						// право выдаётся на подключение целиком: дальше не спрашиваем
						once.Do(func() {
							g.mu.Lock()
							g.forbidden[src.ID] = now.Add(guestForbidden)
							g.mu.Unlock()
							close(stop)
							log.Printf("proxmox %s: у токена нет права читать гостевой агент (Proxmox 9: VM.GuestAgent.Audit, Proxmox 8: VM.Monitor), диски ВМ не видны", src.Name)
						})
					default:
						st = &guestState{status: guestFailed, next: now.Add(guestError), err: err.Error()}
					}
					g.set(j.hostID, st)
					mu.Lock()
					counts[st.status]++
					mu.Unlock()
				}(j)
				continue
			}
			break
		}
		wg.Wait()
		g.mu.Lock()
		if until, ok := g.forbidden[src.ID]; ok && time.Now().After(until) {
			delete(g.forbidden, src.ID)
		}
		g.mu.Unlock()
		if d := time.Since(started); d > 20*time.Second || debugPVE() {
			log.Printf("proxmox %s: гостевые агенты, %d машин за %.1f с: ответили %d, без агента %d, ошибок %d",
				src.Name, len(jobs), d.Seconds(), counts[guestOK], counts[guestMissing], counts[guestFailed])
		}
	}()
}

func debugPVE() bool { return os.Getenv("SENTRA_DEBUG_PVE") != "" }

// guestSamples добавляет диски из гостевого агента к метрикам ВМ.
func guestSamples(fs []proxmox.FS) []proto.Sample {
	var out []proto.Sample
	for _, f := range fs {
		l := map[string]string{"mount": f.Mountpoint}
		free := f.Total - f.Used
		if free < 0 {
			free = 0
		}
		out = append(out,
			proto.Sample{Name: proto.MFsUsedPct, Labels: l, Value: round2(100 * float64(f.Used) / float64(f.Total))},
			proto.Sample{Name: proto.MFsFree, Labels: l, Value: float64(free)},
			proto.Sample{Name: proto.MFsTotal, Labels: l, Value: float64(f.Total)},
		)
	}
	return out
}
