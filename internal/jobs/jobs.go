// Package jobs runs update jobs (spec §7): pre-checks, Proxmox snapshot,
// update (API request then wait for the new version, or script run by the
// agent with its output streamed), healthcheck, version check, snapshot
// retention, optional automatic rollback, and cancellation. Jobs are asynchronous
// and persisted (tables jobs and job_logs). See doc/updates.md.
package jobs

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Wolfx56/lum/internal/agentproto"
	"github.com/Wolfx56/lum/internal/config"
	"github.com/Wolfx56/lum/internal/notify"
	"github.com/Wolfx56/lum/internal/proxmox"
	"github.com/Wolfx56/lum/internal/semver"
	"github.com/Wolfx56/lum/internal/store"
)

// SnapshotPrefix starts the name of every snapshot taken by LUM.
const SnapshotPrefix = "lum-pre-update-"

// ReasonServerRestart is the error of the jobs interrupted by a server stop.
const ReasonServerRestart = "server_restart"

// Pre-check refusals (the job is not created).
var (
	ErrUnknownApp     = errors.New("unknown app")
	ErrNoUpdate       = errors.New("no update recipe for this app")
	ErrNotUpdatable   = errors.New("no update available for this app")
	ErrUnsupported    = errors.New("update type not supported")
	ErrProxmoxMissing = errors.New("snapshot_before_update and update.resources require the proxmox section of server.yaml")
	ErrAgentOffline   = errors.New("agent offline")
	ErrJobActive      = store.ErrJobActive
	// ErrNotRunning is returned by Cancel for a job that is not running in this process.
	ErrNotRunning = errors.New("job not running")
)

// Manual rollback refusals (Rollback, RollbackCheck).
var (
	ErrNoSnapshot        = errors.New("the job has no snapshot")
	ErrJobNotFinished    = errors.New("the job is not finished")
	ErrAlreadyRolledBack = errors.New("the job was already rolled back")
	ErrSnapshotGone      = errors.New("the snapshot no longer exists")
	ErrRollbackRunning   = errors.New("a rollback is in progress for this app")
	ErrProxmox           = errors.New("proxmox error")
)

// Agents runs update scripts on the agents (agenthub.Hub).
type Agents interface {
	Connected(agentID string) bool
	EnsureScript(ctx context.Context, agentID, script string) (sha256 string, err error)
	RunUpdate(ctx context.Context, agentID string, req agentproto.RunUpdate, onLog func(stream, line string)) (agentproto.JobDone, error)
	CancelJob(ctx context.Context, agentID, jobID string) error
}

// Snapshotter manages container snapshots and resources (proxmox.Client).
type Snapshotter interface {
	Snapshot(ctx context.Context, vmid int, name, description string) error
	Rollback(ctx context.Context, vmid int, name string) error
	DeleteSnapshot(ctx context.Context, vmid int, name string) error
	Snapshots(ctx context.Context, vmid int) ([]proxmox.Snapshot, error)
	Resources(ctx context.Context, vmid int) (proxmox.Resources, error)
	SetResources(ctx context.Context, vmid int, r proxmox.Resources) error
}

// CurrentReader reads installed versions (current.Reader).
type CurrentReader interface {
	Current(ctx context.Context, app config.App) (string, error)
}

// Options configures the Manager.
type Options struct {
	Config func() *config.Config
	Store  *store.Store
	// Proxmox returns the snapshot client, nil when proxmox is not configured.
	Proxmox func() Snapshotter
	Current CurrentReader
	// Agents runs update.type script; nil: script updates are refused.
	Agents Agents
	// Check re-checks the app after a job, so that the dashboard shows the new version.
	Check  func(ctx context.Context, appID string)
	Notify func(ctx context.Context, e notify.Event)
	// HTTPClient sends the update requests and healthchecks; nil = 30 s timeout.
	HTTPClient *http.Client
	// Observe is called when a job ends (metrics); may be nil.
	Observe func(typ, status string, d time.Duration)
	Logger  *slog.Logger
	Now     func() time.Time

	// Polling intervals (defaults: 10 s for the version, 5 s for the healthcheck).
	VersionPoll time.Duration
	HealthPoll  time.Duration
}

// Manager starts and tracks the jobs. It is safe for concurrent use.
type Manager struct {
	opts   Options
	ctx    context.Context // cancelled by Stop
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu        sync.Mutex
	running   map[string]*run   // by job id
	rollbacks map[string]string // manual rollbacks in progress: app id → job id

	watchMu sync.Mutex
	watch   map[string]chan struct{} // closed at the next change of the job
}

// New returns a Manager.
func New(opts Options) *Manager {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if opts.VersionPoll <= 0 {
		opts.VersionPoll = 10 * time.Second
	}
	if opts.HealthPoll <= 0 {
		opts.HealthPoll = 5 * time.Second
	}
	if opts.Proxmox == nil {
		opts.Proxmox = func() Snapshotter { return nil }
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{opts: opts, ctx: ctx, cancel: cancel, running: map[string]*run{},
		rollbacks: map[string]string{}, watch: map[string]chan struct{}{}}
}

// Recover marks failed the jobs left active by a previous server process (spec §13).
func (m *Manager) Recover(ctx context.Context) error {
	ids, err := m.opts.Store.FailActiveJobs(ctx, ReasonServerRestart, m.opts.Now())
	for _, id := range ids {
		m.opts.Logger.Warn("job interrupted by a server restart", "job_id", id)
	}
	return err
}

// Stop interrupts the running jobs (they end failed, reason server_restart)
// and waits for them.
func (m *Manager) Stop() {
	m.cancel()
	m.wg.Wait()
}

// Start runs the pre-checks, creates the job and runs it in the background.
func (m *Manager) Start(ctx context.Context, appID, user string) (store.Job, error) {
	cfg := m.opts.Config()
	app, ok := cfg.App(appID)
	if !ok {
		return store.Job{}, fmt.Errorf("%w: %s", ErrUnknownApp, appID)
	}
	if app.Update == nil {
		return store.Job{}, ErrNoUpdate
	}
	if app.NeedsProxmox() && m.opts.Proxmox() == nil {
		return store.Job{}, ErrProxmoxMissing
	}
	switch app.Update.Type {
	case config.TypeAPI:
	case config.TypeScript:
		// Spec §7.1: agent online and script synchronized, before any snapshot.
		if m.opts.Agents == nil || !m.opts.Agents.Connected(app.Agent) {
			return store.Job{}, fmt.Errorf("%w: %s is not connected, nothing was done", ErrAgentOffline, app.Agent)
		}
		if _, err := m.opts.Agents.EnsureScript(ctx, app.Agent, app.Update.Script); err != nil {
			return store.Job{}, err
		}
	default:
		return store.Job{}, fmt.Errorf("%w: %s", ErrUnsupported, app.Update.Type)
	}
	st, err := m.opts.Store.AppState(ctx, appID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && st.Status != string(semver.UpdateAvailable)) {
		return store.Job{}, fmt.Errorf("%w (status %q)", ErrNotUpdatable, st.Status)
	}
	if err != nil {
		return store.Job{}, err
	}

	job := store.Job{
		ID: newUUID(), AppID: appID, Type: app.Update.Type, Status: store.JobPending,
		CurrentVersion: st.CurrentVersion, TargetVersion: st.LatestVersion,
		CreatedBy: user, CreatedAt: m.opts.Now(),
	}
	r := &run{m: m, app: app, job: job, log: m.opts.Logger.With("job_id", job.ID, "app_id", appID)}
	r.ctx, r.cancel = context.WithCancelCause(m.ctx)
	// Under m.mu: a manual rollback of the app cannot start meanwhile.
	m.mu.Lock()
	if _, ok := m.rollbacks[appID]; ok {
		m.mu.Unlock()
		return store.Job{}, fmt.Errorf("%w: %w", ErrJobActive, ErrRollbackRunning)
	}
	if err := m.opts.Store.CreateJob(ctx, job); err != nil {
		m.mu.Unlock()
		return store.Job{}, err
	}
	m.running[job.ID] = r
	m.mu.Unlock()
	r.log.Info("job created", "user", user, "type", job.Type, "from", job.CurrentVersion, "to", job.TargetVersion)
	m.wg.Go(func() {
		defer func() {
			m.mu.Lock()
			delete(m.running, job.ID)
			m.mu.Unlock()
			m.changed(job.ID) // watchers see a job that is no longer busy
		}()
		r.execute()
	})
	return job, nil
}

// Cancel stops a running job: the script receives SIGTERM then SIGKILL after
// 30 s (update.type script), the polling stops (update.type api). The job ends
// cancelled; the snapshot is kept, without automatic rollback.
func (m *Manager) Cancel(ctx context.Context, jobID, user string) error {
	m.mu.Lock()
	r, ok := m.running[jobID]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotRunning, jobID)
	}
	return r.requestCancel(ctx, user)
}

// Running reports whether the job runs in this process (it can be cancelled).
func (m *Manager) Running(jobID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.running[jobID]
	return ok
}

// Busy reports whether the job runs or is being rolled back: its logs and
// status may still change.
func (m *Manager) Busy(jobID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.busyLocked(jobID)
}

func (m *Manager) busyLocked(jobID string) bool {
	if _, ok := m.running[jobID]; ok {
		return true
	}
	for _, id := range m.rollbacks {
		if id == jobID {
			return true
		}
	}
	return false
}

// ActiveCount returns the number of jobs running in this process.
func (m *Manager) ActiveCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.running)
}

// Watch returns a channel closed at the next change of a busy job (new log
// line, new status, end of the work on it), or nil if the job is not busy: it
// will not change any more. Checking and subscribing are atomic: the end of
// the job cannot be missed in between.
func (m *Manager) Watch(jobID string) <-chan struct{} {
	m.mu.Lock() // the job cannot stop being busy meanwhile
	defer m.mu.Unlock()
	if !m.busyLocked(jobID) {
		return nil
	}
	m.watchMu.Lock()
	defer m.watchMu.Unlock()
	ch, ok := m.watch[jobID]
	if !ok {
		ch = make(chan struct{})
		m.watch[jobID] = ch
	}
	return ch
}

func (m *Manager) changed(jobID string) {
	m.watchMu.Lock()
	defer m.watchMu.Unlock()
	if ch, ok := m.watch[jobID]; ok {
		close(ch)
		delete(m.watch, jobID)
	}
}

// SnapshotName is the snapshot of a job. Proxmox limits names to 40
// characters: the prefix is followed by the first 12 hex digits of the job id.
func SnapshotName(jobID string) string {
	id := strings.ReplaceAll(jobID, "-", "")
	return SnapshotPrefix + id[:min(12, len(id))]
}

func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40 // version 4
	b[8] = b[8]&0x3f | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
