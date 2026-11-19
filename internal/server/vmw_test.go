package server

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/session"
	"github.com/vmware/govmomi/simulator"
	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/soap"

	"sentra/internal/vmware"
)

// fakeVM принимает импорт метрик вместо VictoriaMetrics и запоминает строки.
type fakeVM struct {
	mu    sync.Mutex
	lines []string
}

func (f *fakeVM) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.lines = append(f.lines, strings.Split(strings.TrimSpace(string(b)), "\n")...)
	f.mu.Unlock()
	w.WriteHeader(204)
}

func (f *fakeVM) has(sub string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, l := range f.lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

// vcsimClient — отдельный клиент govmomi, чтобы менять симулятор (как админ в vSphere).
func vcsimClient(t *testing.T, u *url.URL) *vim25.Client {
	t.Helper()
	sc := soap.NewClient(u, true)
	vc, err := vim25.NewClient(context.Background(), sc)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.NewManager(vc).Login(context.Background(), u.User); err != nil {
		t.Fatal(err)
	}
	return vc
}

func TestCollectVMware(t *testing.T) {
	m := simulator.VPX()
	m.Cluster = 1 // кластер из трёх хостов и отдельный хост
	defer m.Remove()
	if err := m.Create(); err != nil {
		t.Fatal(err)
	}
	m.Service.TLS = new(tls.Config)
	sim := m.Service.NewServer()
	defer sim.Close()

	fvm := &fakeVM{}
	vmSrv := httptest.NewServer(fvm)
	defer vmSrv.Close()

	st := testStore(t)
	s := &Server{st: st, vm: NewVM(vmSrv.URL), vmw: newVMWPoller(), eng: NewEngine(st)}
	ctx := context.Background()
	base := "https://" + sim.URL.Host
	pass, _ := sim.URL.User.Password()
	fp, err := vmware.FetchFingerprint(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.AddVMW("Тестовый vCenter", base, sim.URL.User.Username(), pass, fp, "VirtualCenter")
	if err != nil {
		t.Fatal(err)
	}
	src, _ := st.VMWSource(id)
	if err := s.collectVMW(ctx, src); err != nil {
		t.Fatal(err)
	}

	hs, _ := st.Hosts()
	views, err := s.views(hs)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	byName := map[string]hostView{}
	for _, v := range views {
		kinds[v.Kind]++
		byName[v.Name] = v
	}
	if kinds[kindVMWHost] != 4 || kinds[kindVMWVM] == 0 {
		t.Fatalf("виды устройств: %v", kinds)
	}
	for _, v := range views {
		if v.Status != "ok" {
			t.Errorf("%s (%s): статус %s, %v", v.Name, v.Kind, v.Status, v.Issues)
		}
	}
	if !fvm.has("sentra_cpu_usage_percent") || !fvm.has(`sentra_fs_used_percent{host=`) {
		t.Error("метрики не дошли до хранилища")
	}
	src, _ = st.VMWSource(id)
	if src.LastOK == 0 || src.Summary["hosts"].(float64) != 4 {
		t.Errorf("итог опроса: %+v", src)
	}

	// общее хранилище кластера: метрики только у одного хоста
	owners := 0
	for _, h := range hs {
		if h.Kind != kindVMWHost {
			continue
		}
		if fs, _ := h.LastMetrics["fs"].(map[string]any); fs["LocalDS_0"] != nil {
			owners++
		}
	}
	if owners != 1 {
		t.Errorf("общее хранилище LocalDS_0 приписано %d хостам, ожидали одному", owners)
	}

	// админ отключает хост кластера от vCenter и выключает одну машину
	vc := vcsimClient(t, sim.URL)
	fd := find.NewFinder(vc, true)
	dc, _ := fd.DefaultDatacenter(ctx)
	fd.SetDatacenter(dc)
	target, _ := byName["DC0_C0_RP0_VM0"].Meta["node"].(string)
	if target == "" {
		t.Fatal("у машины кластера не указан хост")
	}
	host, err := fd.HostSystem(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	task, err := host.Disconnect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	task.Wait(ctx)
	vm, err := fd.VirtualMachine(ctx, "DC0_H0_VM0")
	if err != nil {
		t.Fatal(err)
	}
	task, _ = vm.PowerOff(ctx)
	task.Wait(ctx)

	if err := s.collectVMW(ctx, src); err != nil {
		t.Fatal(err)
	}
	hs, _ = st.Hosts()
	views, _ = s.views(hs)
	for _, v := range views {
		byName[v.Name] = v
	}
	if v := byName[target]; v.Status != "critical" {
		t.Errorf("отключённый хост: %s %v", v.Status, v.Issues)
	}
	if ps, _ := st.OpenProblemsForHost(byName[target].ID); len(ps) != 1 || !strings.HasPrefix(ps[0].Title, "Хост ESXi не на связи с vCenter, с ним недоступны") {
		for _, p := range ps {
			t.Logf("проблема: %s %s", p.Key, p.Title)
		}
		t.Error("ждали одну аварию «Хост ESXi не на связи с vCenter» с числом машин")
	}
	if v := byName["DC0_H0_VM0"]; v.Status != "stopped" {
		t.Errorf("выключенная машина: %s %v", v.Status, v.Issues)
	}
	// машины отключённого хоста: авария зависимая, в общем списке её нет
	dep := 0
	for _, v := range views {
		if v.Kind == kindVMWVM && v.Meta["node"] == target {
			dep++
			if v.Status != "critical" {
				t.Errorf("машина %s на отключённом хосте: %s", v.Name, v.Status)
			}
		}
	}
	if dep == 0 {
		t.Errorf("на хосте %s нет машин", target)
	}

	// машину удалили в vSphere — помечаем «Нет в VMware»
	vm2, _ := fd.VirtualMachine(ctx, "DC0_H0_VM1")
	task, _ = vm2.PowerOff(ctx)
	task.Wait(ctx)
	task, _ = vm2.Destroy(ctx)
	task.Wait(ctx)
	if err := s.collectVMW(ctx, src); err != nil {
		t.Fatal(err)
	}
	hs, _ = st.Hosts()
	views, _ = s.views(hs)
	found := false
	for _, v := range views {
		if v.Name == "DC0_H0_VM1" {
			found = true
			if v.Meta["gone"] != float64(1) || !strings.Contains(strings.Join(v.Issues, " "), "Нет в VMware") {
				t.Errorf("удалённая машина: %v %v", v.Meta["gone"], v.Issues)
			}
		}
	}
	if !found {
		t.Error("удалённая машина пропала из списка, а должна остаться с пометкой")
	}

	// удаление подключения убирает всё
	s.vmw.drop(id)
	if err := st.DeleteVMW(id); err != nil {
		t.Fatal(err)
	}
	hs, _ = st.Hosts()
	if len(hs) != 0 {
		t.Errorf("после удаления подключения осталось %d устройств", len(hs))
	}
}
