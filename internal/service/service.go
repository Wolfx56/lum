// Package service assembles the server components (configuration, store,
// GitHub client, checker, scheduler, agent hub, scripts index, Proxmox client,
// job manager, metrics) and
// applies configuration reloads to them. The HTTP API talks to the Service only.
package service

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wolfx56/lum/internal/agenthub"
	"github.com/Wolfx56/lum/internal/checker"
	"github.com/Wolfx56/lum/internal/config"
	"github.com/Wolfx56/lum/internal/current"
	"github.com/Wolfx56/lum/internal/jobs"
	"github.com/Wolfx56/lum/internal/metrics"
	"github.com/Wolfx56/lum/internal/notify"
	"github.com/Wolfx56/lum/internal/proxmox"
	"github.com/Wolfx56/lum/internal/scheduler"
	"github.com/Wolfx56/lum/internal/scripts"
	"github.com/Wolfx56/lum/internal/semver"
	"github.com/Wolfx56/lum/internal/store"
	"github.com/Wolfx56/lum/internal/upstream"
)

// ErrCheckRunning is returned when a full check is already in progress.
var ErrCheckRunning = errors.New("a full check is already running")

// Options configures the Service.
type Options struct {
	Holder *config.Holder
	Store  *store.Store
	Logger *slog.Logger
	// GitHubURL overrides the GitHub API base URL (tests).
	GitHubURL string
	// HTTPClient is used for app APIs and notifications (tests); nil = defaults.
	HTTPClient *http.Client
	// AgentDir holds the agent binaries served to agents (self_update, install).
	AgentDir string
}

// Service is the application core used by the API.
type Service struct {
	holder  *config.Holder
	store   *store.Store
	logger  *slog.Logger
	ghURL   string
	checker *checker.Checker
	metrics *metrics.Metrics
	hub     *agenthub.Hub
	scripts *scripts.Index
	jobs    *jobs.Manager
	client  *http.Client

	github  atomic.Pointer[upstream.GitHub]
	proxmox atomic.Pointer[proxmox.Client] // nil when not configured

	mu       sync.Mutex // guards reload state below
	ghKey    githubKey
	pxKey    config.Proxmox
	sched    *scheduler.Scheduler
	schedKey scheduleKey

	fullCheck atomic.Bool // a CheckAll is running
	bg        sync.WaitGroup
	cancelBg  context.CancelFunc
}

type githubKey struct {
	token config.Secret
	ttl   time.Duration
}

type scheduleKey struct {
	cron   string
	jitter time.Duration
}

// New builds the components from the active configuration and starts the scheduler.
func New(opts Options) (*Service, error) {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	s := &Service{holder: opts.Holder, store: opts.Store, logger: opts.Logger, ghURL: opts.GitHubURL, client: opts.HTTPClient}
	s.metrics = metrics.New(s)
	s.scripts = scripts.New(opts.Holder.Current().Scripts.Dir, opts.Logger)
	s.hub = agenthub.New(agenthub.Options{
		Config:   opts.Holder.Current,
		Store:    opts.Store,
		Scripts:  s.scripts,
		Notify:   s.notify,
		Logger:   opts.Logger,
		AgentDir: opts.AgentDir,
	})
	reader := current.NewReader(opts.HTTPClient, s.hub)
	s.checker = checker.New(checker.Options{
		Config:     opts.Holder.Current,
		Upstream:   s,
		Current:    reader,
		Store:      opts.Store,
		Logger:     opts.Logger,
		HTTPClient: opts.HTTPClient,
		Observe: func(res store.Check, d time.Duration) {
			s.metrics.ObserveCheck(res.Status, d)
		},
	})
	s.jobs = jobs.New(jobs.Options{
		Config:     opts.Holder.Current,
		Store:      opts.Store,
		Proxmox:    s.snapshotter,
		Current:    reader,
		Agents:     s.hub,
		Check:      s.recheck,
		Notify:     s.notify,
		HTTPClient: opts.HTTPClient,
		Observe:    s.metrics.ObserveJob,
		Logger:     opts.Logger,
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.applyLocked(opts.Holder.Current()); err != nil {
		return nil, err
	}
	return s, nil
}

// snapshotter returns the Proxmox client, nil when proxmox is not configured.
func (s *Service) snapshotter() jobs.Snapshotter {
	if c := s.proxmox.Load(); c != nil {
		return c
	}
	return nil
}

// recheck refreshes an app after a job, so that the dashboard shows the result.
func (s *Service) recheck(ctx context.Context, appID string) {
	if _, err := s.checker.CheckApp(ctx, appID); err != nil {
		s.logger.Warn("check after job failed", "app_id", appID, "error", err.Error())
	}
}

// Start runs the startup purge and full check, the agent offline detection and
// the scripts watcher in the background.
func (s *Service) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	s.cancelBg = cancel
	// Jobs left active by the previous process cannot be resumed (spec §13).
	if err := s.jobs.Recover(ctx); err != nil {
		s.logger.Error("interrupted jobs not marked failed", "error", err.Error())
	}
	s.bg.Go(func() { s.logProxmox(ctx) })
	s.bg.Go(func() { s.hub.Run(ctx) })
	s.bg.Go(func() {
		if err := s.scripts.Watch(ctx, func() { s.hub.SyncAll(ctx) }); err != nil {
			s.logger.Error("scripts watcher not started", "error", err.Error())
		}
	})
	s.bg.Go(func() {
		scheduler.Purge(ctx, s.store, s.logger)
		if _, err := s.CheckAll(ctx); err != nil {
			s.logger.Warn("startup check skipped", "error", err.Error())
		}
	})
}

// Stop stops the scheduler, closes the agent connections and waits for background work.
func (s *Service) Stop() {
	if s.cancelBg != nil {
		s.cancelBg()
	}
	s.jobs.Stop()
	s.hub.CloseAll()
	s.mu.Lock()
	if s.sched != nil {
		s.sched.Stop()
	}
	s.mu.Unlock()
	s.bg.Wait()
}

// Config returns the active configuration.
func (s *Service) Config() *config.Config { return s.holder.Current() }

// Store returns the database.
func (s *Service) Store() *store.Store { return s.store }

// Jobs returns the update job manager.
func (s *Service) Jobs() *jobs.Manager { return s.jobs }

// Hub returns the agent hub (WebSocket endpoint).
func (s *Service) Hub() *agenthub.Hub { return s.hub }

// Metrics returns the Prometheus metrics.
func (s *Service) Metrics() *metrics.Metrics { return s.metrics }

// ConfigStatus returns when the active configuration was loaded and the last reload error.
func (s *Service) ConfigStatus() (time.Time, error) { return s.holder.Status() }

// Reload re-reads the configuration. On success the GitHub client and the
// scheduler are rebuilt if their settings changed. On error nothing changes.
// Settings read at startup only (listen, log level, auth, data dir) need a restart.
func (s *Service) Reload() error {
	if err := s.holder.Reload(); err != nil {
		s.logger.Error("configuration reload failed, previous configuration kept", "error", err.Error())
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg := s.holder.Current()
	if err := s.applyLocked(cfg); err != nil {
		return err
	}
	s.logger.Info("configuration reloaded", "apps", len(cfg.Apps))
	// Recipes (hence the scripts each agent needs) or scripts.dir may have changed.
	s.scripts.SetDir(cfg.Scripts.Dir)
	s.hub.SyncAll(context.Background())
	return nil
}

// notify dispatches an event to the configured channels (agent_offline).
func (s *Service) notify(ctx context.Context, e notify.Event) {
	if err := notify.FromConfig(s.Config().Notifications, s.client).Dispatch(ctx, e); err != nil {
		s.logger.Warn("notification failed", "event", e.Type, "error", err.Error())
	}
}

// applyLocked (re)builds the components depending on the configuration.
func (s *Service) applyLocked(cfg *config.Config) error {
	gk := githubKey{token: cfg.GitHub.Token, ttl: cfg.GitHub.CacheTTL}
	if s.github.Load() == nil || gk != s.ghKey {
		s.github.Store(upstream.NewGitHub(upstream.GitHubOptions{Token: gk.token, CacheTTL: gk.ttl, BaseURL: s.ghURL}))
		s.ghKey = gk
	}

	if s.proxmox.Load() == nil || cfg.Proxmox != s.pxKey {
		if cfg.Proxmox.Configured() {
			s.proxmox.Store(proxmox.New(cfg.Proxmox))
		} else {
			s.proxmox.Store(nil)
		}
		s.pxKey = cfg.Proxmox
	}

	sk := scheduleKey{cron: cfg.Scheduler.CheckCron, jitter: cfg.Scheduler.Jitter}
	if s.sched == nil || sk != s.schedKey {
		next, err := scheduler.Start(scheduler.Options{
			CheckCron: sk.cron,
			Jitter:    sk.jitter,
			Runner:    scheduledRunner{s},
			Purger:    s.store,
			Logger:    s.logger,
		})
		if err != nil {
			return err
		}
		if s.sched != nil {
			s.sched.Stop()
		}
		s.sched, s.schedKey = next, sk
	}
	return nil
}

// Latest implements upstream.Source with the current GitHub client.
func (s *Service) Latest(ctx context.Context, u config.Upstream) (upstream.Release, error) {
	return s.github.Load().Latest(ctx, u)
}

// RateLimit returns the GitHub quota.
func (s *Service) RateLimit() upstream.RateLimit { return s.github.Load().RateLimit() }

// NextCheck returns the next scheduled check.
func (s *Service) NextCheck() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sched.NextCheck()
}

// CheckApp checks one app now.
func (s *Service) CheckApp(ctx context.Context, appID string) (store.Check, error) {
	return s.checker.CheckApp(ctx, appID)
}

// CheckAll checks every app now; ErrCheckRunning if one is already running.
func (s *Service) CheckAll(ctx context.Context) ([]store.Check, error) {
	if !s.fullCheck.CompareAndSwap(false, true) {
		return nil, ErrCheckRunning
	}
	defer s.fullCheck.Store(false)
	return s.checker.CheckAll(ctx), nil
}

// CheckRunning reports whether a full check is in progress.
func (s *Service) CheckRunning() bool { return s.fullCheck.Load() }

// AppStatuses counts the configured apps by status (never checked = unknown).
func (s *Service) AppStatuses() map[string]int {
	counts := map[string]int{}
	states, err := s.store.AppStates(context.Background())
	if err != nil {
		return counts
	}
	for _, a := range s.Config().Apps {
		st, ok := states[a.ID]
		if !ok {
			counts[string(semver.Unknown)]++
			continue
		}
		counts[st.Status]++
	}
	return counts
}

// AgentStatuses counts the enrolled agents by status (online, offline, revoked).
func (s *Service) AgentStatuses() map[string]int {
	counts := map[string]int{}
	agents, err := s.store.Agents(context.Background())
	if err != nil {
		return counts
	}
	for _, a := range agents {
		if a.Revoked {
			counts["revoked"]++
			continue
		}
		counts[a.Status]++
	}
	return counts
}

// ActiveJobs returns the number of running jobs (metrics).
func (s *Service) ActiveJobs() int { return s.jobs.ActiveCount() }

// ConnectedAgents returns the number of connected agents (metrics).
func (s *Service) ConnectedAgents() int { return s.hub.ConnectedCount() }

// scheduledRunner adapts the Service to scheduler.Runner, sharing the guard
// against concurrent full checks.
type scheduledRunner struct{ s *Service }

func (r scheduledRunner) CheckAll(ctx context.Context) []store.Check {
	res, err := r.s.CheckAll(ctx)
	if err != nil {
		r.s.logger.Info("scheduled check skipped", "reason", err.Error())
	}
	return res
}
