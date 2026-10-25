package jobs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wolfx56/lum/internal/config"
	"github.com/Wolfx56/lum/internal/current"
	"github.com/Wolfx56/lum/internal/notify"
	"github.com/Wolfx56/lum/internal/proxmox"
	"github.com/Wolfx56/lum/internal/store"
)

const apiKey = "test-sonarr-key"

// fakeApp is a Sonarr that installs 4.0.10 when it receives ApplicationUpdate.
type fakeApp struct {
	mu          sync.Mutex
	version     string
	upgradeTo   string // version installed by the update ("" = the update does nothing)
	updates     int
	updateBody  string
	pingStatus  int
	updateCode  int
	versionCall atomic.Int32
}

func (f *fakeApp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path != "/ping" && r.Header.Get("X-Api-Key") != apiKey {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch r.URL.Path {
	case "/api/v3/system/status":
		f.versionCall.Add(1)
		fmt.Fprintf(w, `{"version":%q}`, f.version)
	case "/api/v3/command":
		body, _ := io.ReadAll(r.Body)
		f.updates++
		f.updateBody = string(body)
		if f.updateCode != 0 {
			w.WriteHeader(f.updateCode)
			return
		}
		if f.upgradeTo != "" {
			f.version = f.upgradeTo
		}
		w.WriteHeader(http.StatusCreated)
	case "/ping":
		w.WriteHeader(f.pingStatus)
	}
}

// fakePVE records the snapshot operations.
type fakePVE struct {
	mu        sync.Mutex
	snapshots []proxmox.Snapshot
	calls     []string
	failSnap  error
	resources proxmox.Resources
	failSet   int // the failSet-th SetResources fails (1 = the first)
	sets      int
	// rollbackGate, if set, holds Rollback until closed.
	rollbackGate chan struct{}
}

func (p *fakePVE) Snapshot(_ context.Context, vmid int, name, desc string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, fmt.Sprintf("snapshot %d %s", vmid, name))
	if p.failSnap != nil {
		return p.failSnap
	}
	p.snapshots = append(p.snapshots, proxmox.Snapshot{Name: name, Description: desc, Time: time.Now()})
	return nil
}

func (p *fakePVE) Rollback(_ context.Context, vmid int, name string) error {
	p.mu.Lock()
	p.calls = append(p.calls, fmt.Sprintf("rollback %d %s", vmid, name))
	gate := p.rollbackGate
	p.mu.Unlock()
	if gate != nil {
		<-gate
	}
	return nil
}

func (p *fakePVE) DeleteSnapshot(_ context.Context, vmid int, name string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, fmt.Sprintf("delete %d %s", vmid, name))
	p.snapshots = slices.DeleteFunc(p.snapshots, func(s proxmox.Snapshot) bool { return s.Name == name })
	return nil
}

func (p *fakePVE) Resources(context.Context, int) (proxmox.Resources, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.resources, nil
}

func (p *fakePVE) SetResources(_ context.Context, vmid int, r proxmox.Resources) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sets++
	p.calls = append(p.calls, fmt.Sprintf("resources %d cores=%d memory=%d", vmid, r.Cores, r.Memory))
	if p.sets == p.failSet {
		return errors.New("permission check failed (/vms/101, VM.Config.Memory)")
	}
	if r.Cores > 0 {
		p.resources.Cores = r.Cores
	}
	if r.Memory > 0 {
		p.resources.Memory = r.Memory
	}
	return nil
}

func (p *fakePVE) Snapshots(context.Context, int) ([]proxmox.Snapshot, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.snapshots), nil
}

func (p *fakePVE) called(prefix string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for _, c := range p.calls {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

type env struct {
	t       *testing.T
	app     *fakeApp
	pve     *fakePVE
	store   *store.Store
	cfg     atomic.Pointer[config.Config]
	mgr     *Manager
	checked atomic.Int32

	mu     sync.Mutex
	events []notify.Event
}

func newEnv(t *testing.T, edit func(*config.App)) *env {
	t.Helper()
	e := &env{t: t, app: &fakeApp{version: "4.0.9", upgradeTo: "4.0.10", pingStatus: http.StatusOK}, pve: &fakePVE{}}
	srv := httptest.NewServer(e.app)
	t.Cleanup(srv.Close)

	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "lum.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	e.store = db

	app := config.App{
		ID: "sonarr", Name: "Sonarr", LXCID: 101, Retention: 2, SnapshotBeforeUpdate: true,
		Current: config.Current{Type: config.TypeAPI, URL: srv.URL + "/api/v3/system/status", AuthHeader: "X-Api-Key", Token: apiKey, JSONPath: "$.version"},
		Update: &config.Update{Type: config.TypeAPI, Method: http.MethodPost, URL: srv.URL + "/api/v3/command", AuthHeader: "X-Api-Key", Token: apiKey,
			Body: `{"name": "ApplicationUpdate"}`, Timeout: 5 * time.Second},
		Healthcheck: &config.Healthcheck{URL: srv.URL + "/ping", ExpectStatus: http.StatusOK, Timeout: 300 * time.Millisecond},
	}
	if edit != nil {
		edit(&app)
	}
	e.cfg.Store(&config.Config{Apps: []config.App{app}})

	if _, err := db.RecordCheck(context.Background(), store.Check{AppID: "sonarr", CheckedAt: time.Now(),
		CurrentVersion: "4.0.9", LatestVersion: "4.0.10", Status: "update_available"}); err != nil {
		t.Fatal(err)
	}

	e.mgr = New(Options{
		Config:  func() *config.Config { return e.cfg.Load() },
		Store:   db,
		Proxmox: func() Snapshotter { return e.pve },
		Current: current.NewReader(nil, nil),
		Check:   func(context.Context, string) { e.checked.Add(1) },
		Notify: func(_ context.Context, ev notify.Event) {
			e.mu.Lock()
			e.events = append(e.events, ev)
			e.mu.Unlock()
		},
		VersionPoll: 10 * time.Millisecond,
		HealthPoll:  10 * time.Millisecond,
	})
	t.Cleanup(e.mgr.Stop)
	return e
}

// wait returns the job once it reached a final state.
func (e *env) wait(id string) store.Job {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		j, err := e.store.Job(context.Background(), id)
		if err != nil {
			e.t.Fatal(err)
		}
		if !slices.Contains(store.ActiveJobStates, j.Status) {
			e.mgr.wg.Wait() // the end of the job (notification, check) is done too
			return j
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("job still %s", j.Status)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (e *env) start() store.Job {
	e.t.Helper()
	j, err := e.mgr.Start(context.Background(), "sonarr", "alice")
	if err != nil {
		e.t.Fatal(err)
	}
	return e.wait(j.ID)
}

func (e *env) logs(id string) string {
	e.t.Helper()
	logs, err := e.store.JobLogs(context.Background(), id, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	var b strings.Builder
	for _, l := range logs {
		b.WriteString(l.Line + "\n")
	}
	return b.String()
}

func (e *env) eventTypes() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, ev := range e.events {
		out = append(out, ev.Type)
	}
	return out
}

func TestUpdateSuccess(t *testing.T) {
	e := newEnv(t, nil)
	// Three older LUM snapshots and a manual one: retention 2 keeps the job's and the newest old one.
	old := time.Now().Add(-time.Hour)
	e.pve.snapshots = []proxmox.Snapshot{
		{Name: "lum-pre-update-aaaaaaaaaaaa", Time: old.Add(-2 * time.Hour)},
		{Name: "lum-pre-update-bbbbbbbbbbbb", Time: old.Add(-time.Hour)},
		{Name: "lum-pre-update-cccccccccccc", Time: old},
		{Name: "manual-before-upgrade", Time: old.Add(-3 * time.Hour)},
	}

	j := e.start()
	if j.Status != store.JobSuccess || j.Error != "" || j.CreatedBy != "alice" || j.CurrentVersion != "4.0.9" || j.TargetVersion != "4.0.10" {
		t.Fatalf("job = %+v", j)
	}
	if j.SnapshotName != SnapshotName(j.ID) || len(j.SnapshotName) > 40 || j.StartedAt.IsZero() || j.FinishedAt.IsZero() {
		t.Errorf("job = %+v", j)
	}
	if e.app.updates != 1 || e.app.updateBody != `{"name": "ApplicationUpdate"}` {
		t.Errorf("update request: %d, %q", e.app.updates, e.app.updateBody)
	}
	if got := e.pve.called("snapshot"); len(got) != 1 || got[0] != "snapshot 101 "+j.SnapshotName {
		t.Errorf("snapshot calls = %v", got)
	}
	deleted := e.pve.called("delete")
	if len(deleted) != 2 || !slices.Contains(deleted, "delete 101 lum-pre-update-aaaaaaaaaaaa") || !slices.Contains(deleted, "delete 101 lum-pre-update-bbbbbbbbbbbb") {
		t.Errorf("retention deleted %v", deleted)
	}
	if types := e.eventTypes(); len(types) != 1 || types[0] != config.EventJobSuccess {
		t.Errorf("notifications = %v", types)
	}
	if e.checked.Load() != 1 {
		t.Error("the app must be re-checked after the job")
	}
	logs := e.logs(j.ID)
	for _, want := range []string{"Snapshot created", "Request accepted (HTTP 201)", "New version detected: 4.0.10", "Healthcheck OK", "Version verified: 4.0.10", "Update succeeded"} {
		if !strings.Contains(logs, want) {
			t.Errorf("logs miss %q:\n%s", want, logs)
		}
	}
	if strings.Contains(logs, apiKey) {
		t.Error("logs must not contain the API key")
	}
}

func TestHealthcheckFailureKeepsSnapshot(t *testing.T) {
	e := newEnv(t, nil)
	e.app.pingStatus = http.StatusServiceUnavailable
	j := e.start()
	if j.Status != store.JobFailed || !strings.Contains(j.Error, "healthcheck") || !strings.Contains(j.Error, "HTTP 503") {
		t.Fatalf("job = %+v", j)
	}
	if got := e.pve.called("rollback"); len(got) != 0 {
		t.Errorf("no automatic rollback without rollback_on_failure: %v", got)
	}
	if got := e.pve.called("delete"); len(got) != 0 {
		t.Errorf("the snapshot must be kept for a manual rollback: %v", got)
	}
	if types := e.eventTypes(); len(types) != 1 || types[0] != config.EventJobFailed {
		t.Errorf("notifications = %v", types)
	}
	if !strings.Contains(e.logs(j.ID), "manual rollback possible") {
		t.Error("logs must mention the manual rollback")
	}
}

func TestAutomaticRollback(t *testing.T) {
	e := newEnv(t, func(a *config.App) { a.RollbackOnFailure = true })
	e.app.pingStatus = http.StatusInternalServerError
	j := e.start()
	if j.Status != store.JobRolledBack || !strings.Contains(j.Error, "healthcheck") {
		t.Fatalf("job = %+v", j)
	}
	if got := e.pve.called("rollback"); len(got) != 1 || got[0] != "rollback 101 "+j.SnapshotName {
		t.Errorf("rollback calls = %v", got)
	}
}

func TestSnapshotFailureCancelsBeforeUpdate(t *testing.T) {
	e := newEnv(t, func(a *config.App) { a.RollbackOnFailure = true })
	e.pve.failSnap = errors.New("snapshot feature is not available")
	j := e.start()
	if j.Status != store.JobCancelled || !strings.Contains(j.Error, "snapshot feature is not available") {
		t.Fatalf("job = %+v", j)
	}
	if e.app.updates != 0 {
		t.Error("the update must not be sent when the snapshot failed")
	}
	if got := e.pve.called("rollback"); len(got) != 0 {
		t.Errorf("nothing to roll back: %v", got)
	}
}

func TestUnchangedVersionTimesOut(t *testing.T) {
	e := newEnv(t, func(a *config.App) { a.Update.Timeout = 200 * time.Millisecond })
	e.app.upgradeTo = "" // the request is accepted but nothing is installed
	j := e.start()
	if j.Status != store.JobFailed || !strings.HasPrefix(j.Error, "timeout") {
		t.Fatalf("job = %+v", j)
	}
}

func TestWrongVersionFails(t *testing.T) {
	e := newEnv(t, nil)
	e.app.upgradeTo = "4.0.9.1" // changed, but older than the target
	j := e.start()
	if j.Status != store.JobFailed || !strings.Contains(j.Error, "installed version is 4.0.9.1, 4.0.10 expected") {
		t.Fatalf("job = %+v", j)
	}
}

func TestRefusedUpdateRequest(t *testing.T) {
	e := newEnv(t, func(a *config.App) { a.SnapshotBeforeUpdate = false })
	e.app.updateCode = http.StatusUnauthorized
	j := e.start()
	if j.Status != store.JobFailed || !strings.Contains(j.Error, "unexpected status 401") || j.SnapshotName != "" {
		t.Fatalf("job = %+v", j)
	}
	if got := e.pve.called("snapshot"); len(got) != 0 {
		t.Errorf("snapshot_before_update false: no snapshot expected, got %v", got)
	}
}

func TestOneActiveJobPerApp(t *testing.T) {
	e := newEnv(t, func(a *config.App) { a.Update.Timeout = 300 * time.Millisecond })
	e.app.upgradeTo = "" // keeps the first job running until its timeout
	first, err := e.mgr.Start(context.Background(), "sonarr", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.mgr.Start(context.Background(), "sonarr", "bob"); !errors.Is(err, ErrJobActive) {
		t.Fatalf("second start: err = %v, want ErrJobActive", err)
	}
	e.wait(first.ID)
}

func TestPrechecks(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	if _, err := e.mgr.Start(ctx, "ghost", "alice"); !errors.Is(err, ErrUnknownApp) {
		t.Errorf("unknown app: %v", err)
	}

	e.cfg.Store(&config.Config{Apps: []config.App{{ID: "sonarr", Update: &config.Update{Type: config.TypeScript, Script: "x.sh"}}}})
	if _, err := e.mgr.Start(ctx, "sonarr", "alice"); !errors.Is(err, ErrAgentOffline) {
		t.Errorf("script update without agent: %v", err)
	}
	e.cfg.Store(&config.Config{Apps: []config.App{{ID: "sonarr"}}})
	if _, err := e.mgr.Start(ctx, "sonarr", "alice"); !errors.Is(err, ErrNoUpdate) {
		t.Errorf("no update recipe: %v", err)
	}

	e.cfg.Store(&config.Config{Apps: []config.App{{ID: "sonarr", SnapshotBeforeUpdate: true, Update: &config.Update{Type: config.TypeAPI}}}})
	e.mgr.opts.Proxmox = func() Snapshotter { return nil }
	if _, err := e.mgr.Start(ctx, "sonarr", "alice"); !errors.Is(err, ErrProxmoxMissing) {
		t.Errorf("snapshot without proxmox: %v", err)
	}

	e.cfg.Store(&config.Config{Apps: []config.App{{ID: "sonarr", Update: &config.Update{Type: config.TypeAPI}}}})
	if _, err := e.store.RecordCheck(ctx, store.Check{AppID: "sonarr", CheckedAt: time.Now(), CurrentVersion: "4.0.10", LatestVersion: "4.0.10", Status: "up_to_date"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.mgr.Start(ctx, "sonarr", "alice"); !errors.Is(err, ErrNotUpdatable) {
		t.Errorf("up to date app: %v", err)
	}
	if jobs, _ := e.store.Jobs(ctx, "sonarr", 10); len(jobs) != 0 {
		t.Errorf("a refused job must not be created: %+v", jobs)
	}
}

func TestServerStopAndRestart(t *testing.T) {
	e := newEnv(t, nil)
	e.app.upgradeTo = ""
	j, err := e.mgr.Start(context.Background(), "sonarr", "alice")
	if err != nil {
		t.Fatal(err)
	}
	for e.app.versionCall.Load() == 0 { // the job is polling the version
		time.Sleep(5 * time.Millisecond)
	}
	e.mgr.Stop()
	got, _ := e.store.Job(context.Background(), j.ID)
	if got.Status != store.JobFailed || got.Error != ReasonServerRestart {
		t.Fatalf("interrupted job = %+v", got)
	}

	// A job left active by a crash is failed at the next start.
	if err := e.store.CreateJob(context.Background(), store.Job{ID: "crashed", AppID: "radarr", Type: "api", CreatedBy: "x", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := New(Options{Store: e.store}).Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.store.Job(context.Background(), "crashed"); got.Status != store.JobFailed || got.Error != ReasonServerRestart {
		t.Errorf("crashed job = %+v", got)
	}
}

func withResources(a *config.App) {
	a.Update.Resources = &config.Resources{Cores: 4, Memory: 4096}
}

func TestResourcesRaisedThenRestored(t *testing.T) {
	e := newEnv(t, withResources)
	e.pve.resources = proxmox.Resources{Cores: 1, Memory: 512}
	j := e.start()
	if j.Status != store.JobSuccess {
		t.Fatalf("job = %+v", j)
	}
	want := []string{"snapshot 101 " + j.SnapshotName, "resources 101 cores=4 memory=4096", "resources 101 cores=1 memory=512"}
	if got := slices.DeleteFunc(e.pve.called(""), func(c string) bool { return strings.HasPrefix(c, "delete") }); !slices.Equal(got, want) {
		t.Errorf("proxmox calls = %v, want %v (raised after the snapshot, so that a rollback restores the usual resources)", got, want)
	}
	logs := e.logs(j.ID)
	raised := strings.Index(logs, "Raising LXC 101 resources for the update: 4 CPU, 4096 MiB (instead of 1 CPU, 512 MiB)")
	restored := strings.Index(logs, "LXC 101 resources restored: 1 CPU, 512 MiB")
	health := strings.Index(logs, "Healthcheck OK")
	if raised < 0 || restored < raised || health < restored {
		t.Errorf("raise, restore, then healthcheck expected:\n%s", logs)
	}
}

func TestResourcesOnlyRaised(t *testing.T) {
	e := newEnv(t, withResources)
	e.pve.resources = proxmox.Resources{Cores: 0, Memory: 8192} // all the host's cores, more memory than asked
	j := e.start()
	if j.Status != store.JobSuccess || len(e.pve.called("resources")) != 0 {
		t.Fatalf("job = %+v, calls = %v", j, e.pve.called("resources"))
	}
	if !strings.Contains(e.logs(j.ID), "sufficient (all host CPUs, 8192 MiB): unchanged") {
		t.Errorf("logs:\n%s", e.logs(j.ID))
	}

	e = newEnv(t, withResources)
	e.pve.resources = proxmox.Resources{Cores: 8, Memory: 1024}
	if j := e.start(); j.Status != store.JobSuccess {
		t.Fatalf("job = %+v", j)
	}
	if got := e.pve.called("resources"); !slices.Equal(got, []string{"resources 101 cores=0 memory=4096", "resources 101 cores=0 memory=1024"}) {
		t.Errorf("only the memory must change: %v", got)
	}
}

func TestResourcesRestoredOnFailure(t *testing.T) {
	e := newEnv(t, func(a *config.App) {
		withResources(a)
		a.RollbackOnFailure = true
	})
	e.pve.resources = proxmox.Resources{Cores: 2, Memory: 1024}
	e.app.updateCode = http.StatusUnauthorized
	j := e.start()
	if j.Status != store.JobRolledBack {
		t.Fatalf("job = %+v", j)
	}
	if e.pve.resources != (proxmox.Resources{Cores: 2, Memory: 1024}) {
		t.Errorf("resources = %+v", e.pve.resources)
	}
}

func TestResourcesRestoreFailureKeepsSuccess(t *testing.T) {
	e := newEnv(t, withResources)
	e.pve.resources = proxmox.Resources{Cores: 1, Memory: 512}
	e.pve.failSet = 2
	j := e.start()
	if j.Status != store.JobSuccess {
		t.Fatalf("job = %+v", j)
	}
	if !strings.Contains(e.logs(j.ID), "Restore them by hand: pct set 101 --cores 1 --memory 512") {
		t.Errorf("logs must give the command to restore:\n%s", e.logs(j.ID))
	}
}

func TestResourcesRaiseFailureCancels(t *testing.T) {
	e := newEnv(t, withResources)
	e.pve.resources = proxmox.Resources{Cores: 1, Memory: 512}
	e.pve.failSet = 1
	j := e.start()
	if j.Status != store.JobCancelled || !strings.Contains(j.Error, "resources not raised, update not started") || !strings.Contains(j.Error, "VM.Config.Memory") {
		t.Fatalf("job = %+v", j)
	}
	if e.app.updates != 0 || len(e.pve.called("resources")) != 1 {
		t.Errorf("updates = %d, calls = %v", e.app.updates, e.pve.called("resources"))
	}
}

func TestResourcesNeedProxmox(t *testing.T) {
	e := newEnv(t, func(a *config.App) {
		withResources(a)
		a.SnapshotBeforeUpdate = false
	})
	e.mgr.opts.Proxmox = func() Snapshotter { return nil }
	if _, err := e.mgr.Start(context.Background(), "sonarr", "alice"); !errors.Is(err, ErrProxmoxMissing) {
		t.Errorf("resources without proxmox: %v", err)
	}
}

func TestResourcesLeftRaisedOnServerStop(t *testing.T) {
	e := newEnv(t, withResources)
	e.pve.resources = proxmox.Resources{Cores: 1, Memory: 512}
	e.app.upgradeTo = ""
	j, err := e.mgr.Start(context.Background(), "sonarr", "alice")
	if err != nil {
		t.Fatal(err)
	}
	for e.app.versionCall.Load() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	e.mgr.Stop()
	if e.pve.resources != (proxmox.Resources{Cores: 4, Memory: 4096}) {
		t.Errorf("a script may still run on the agent: resources must stay raised, got %+v", e.pve.resources)
	}
	if !strings.Contains(e.logs(j.ID), "restore them by hand once the script is done: pct set 101 --cores 1 --memory 512") {
		t.Errorf("logs:\n%s", e.logs(j.ID))
	}
}

func TestSnapshotName(t *testing.T) {
	name := SnapshotName("1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d")
	if name != "lum-pre-update-1a2b3c4d5e6f" {
		t.Errorf("SnapshotName = %q", name)
	}
	if id := newUUID(); len(id) != 36 || id[14] != '4' {
		t.Errorf("newUUID = %q", id)
	}
}
