// Package checker runs version checks: it collects the installed and latest
// versions of each app, persists the result and notifies new updates.
// See doc/checks-notifications.md.
package checker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/Wolfx56/lum/internal/config"
	"github.com/Wolfx56/lum/internal/current"
	"github.com/Wolfx56/lum/internal/notify"
	"github.com/Wolfx56/lum/internal/semver"
	"github.com/Wolfx56/lum/internal/store"
	"github.com/Wolfx56/lum/internal/upstream"
)

// DefaultAppTimeout bounds the whole check of one app (installed + latest).
const DefaultAppTimeout = 60 * time.Second

// ErrUnknownApp is returned when the app id is not in the configuration.
var ErrUnknownApp = errors.New("unknown app")

// CurrentReader reads installed versions (current.Reader).
type CurrentReader interface {
	Current(ctx context.Context, app config.App) (string, error)
}

// Options configures a Checker.
type Options struct {
	Config     func() *config.Config // active configuration (config.Holder.Current)
	Upstream   upstream.Source
	Current    CurrentReader
	Store      *store.Store // may be nil for Collect-only use (cmd/lum-check)
	Logger     *slog.Logger
	HTTPClient *http.Client // for notifications; nil = default
	AppTimeout time.Duration
	Now        func() time.Time
	// Observe, if set, is called after each app check (metrics).
	Observe func(res store.Check, d time.Duration)
}

// Checker checks app versions. It is safe for concurrent use; concurrent
// checks of the same app are serialized.
type Checker struct {
	opts Options

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// New returns a Checker.
func New(opts Options) *Checker {
	if opts.AppTimeout <= 0 {
		opts.AppTimeout = DefaultAppTimeout
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	return &Checker{opts: opts, locks: map[string]*sync.Mutex{}}
}

// Collect reads both versions of app and computes its status, without
// persisting or notifying anything.
func (c *Checker) Collect(ctx context.Context, app config.App) store.Check {
	ctx, cancel := context.WithTimeout(ctx, c.opts.AppTimeout)
	defer cancel()

	res := store.Check{AppID: app.ID, CheckedAt: c.opts.Now()}

	var installed string
	var curErr error
	var rel upstream.Release
	var upErr error
	var wg sync.WaitGroup
	wg.Go(func() { installed, curErr = c.opts.Current.Current(ctx, app) })
	wg.Go(func() { rel, upErr = c.opts.Upstream.Latest(ctx, app.Upstream) })
	wg.Wait()

	res.CurrentVersion = installed
	res.LatestVersion = rel.Version
	res.ReleaseURL = rel.URL
	res.ReleaseNotes = rel.Body
	res.Status = string(semver.Evaluate(installed, rel.Version))

	var errs []error
	if upErr != nil {
		errs = append(errs, fmt.Errorf("latest version: %w", upErr))
	}
	if curErr != nil && !errors.Is(curErr, current.ErrAgentUnavailable) {
		errs = append(errs, fmt.Errorf("installed version: %w", curErr))
	}
	if len(errs) > 0 {
		res.Status = string(semver.Error)
		res.Error = errors.Join(errs...).Error()
	} else if curErr != nil {
		res.Error = curErr.Error() // agent not connected: status stays unknown (spec §5.1)
	}
	return res
}

// CheckApp checks one app by id, persists the result and notifies a new update.
func (c *Checker) CheckApp(ctx context.Context, appID string) (store.Check, error) {
	cfg := c.opts.Config()
	app, ok := cfg.App(appID)
	if !ok {
		return store.Check{}, fmt.Errorf("%w: %s", ErrUnknownApp, appID)
	}
	return c.check(ctx, cfg, app)
}

// CheckAll checks every app in parallel (at most scheduler.concurrency at a
// time). A failing app never prevents the others from being checked.
func (c *Checker) CheckAll(ctx context.Context) []store.Check {
	cfg := c.opts.Config()
	start := c.opts.Now()
	results := make([]store.Check, len(cfg.Apps))

	var g errgroup.Group
	g.SetLimit(cfg.Scheduler.Concurrency)
	for i, app := range cfg.Apps {
		g.Go(func() error {
			res, err := c.check(ctx, cfg, app)
			if err != nil {
				c.opts.Logger.Error("check not persisted", "app_id", app.ID, "error", err.Error())
			}
			results[i] = res
			return nil
		})
	}
	_ = g.Wait()

	counts := map[string]int{}
	for _, r := range results {
		counts[r.Status]++
	}
	c.opts.Logger.Info("check completed", "apps", len(results), "duration", time.Since(start).String(),
		"up_to_date", counts[string(semver.UpToDate)], "update_available", counts[string(semver.UpdateAvailable)],
		"unknown", counts[string(semver.Unknown)], "error", counts[string(semver.Error)])
	return results
}

func (c *Checker) check(ctx context.Context, cfg *config.Config, app config.App) (store.Check, error) {
	lock := c.appLock(app.ID)
	lock.Lock()
	defer lock.Unlock()

	start := time.Now()
	res := c.Collect(ctx, app)
	if c.opts.Observe != nil {
		c.opts.Observe(res, time.Since(start))
	}
	log := c.opts.Logger.With("app_id", app.ID)
	if res.Status == string(semver.Error) {
		log.Warn("app check failed", "error", res.Error)
	} else {
		log.Debug("app checked", "status", res.Status, "installed", res.CurrentVersion, "latest", res.LatestVersion)
	}

	prev, err := c.opts.Store.RecordCheck(ctx, res)
	if err != nil {
		return res, fmt.Errorf("recording check: %w", err)
	}

	// Notify once per new version: on the transition to update_available, or
	// when an even newer version appears (spec §5.3, last_notified_version).
	if res.Status == string(semver.UpdateAvailable) && res.LatestVersion != prev.LastNotifiedVersion {
		e := notify.Event{
			Type:           config.EventUpdateAvailable,
			AppID:          app.ID,
			AppName:        app.Name,
			CurrentVersion: res.CurrentVersion,
			LatestVersion:  res.LatestVersion,
			ReleaseURL:     res.ReleaseURL,
			Title:          app.Name + ": update available",
			Message:        res.CurrentVersion + " → " + res.LatestVersion,
			Time:           res.CheckedAt,
		}
		if err := notify.FromConfig(cfg.Notifications, c.opts.HTTPClient).Dispatch(ctx, e); err != nil {
			log.Warn("notification failed", "event", e.Type, "error", err.Error())
		} else {
			log.Info("update notified", "installed", res.CurrentVersion, "latest", res.LatestVersion)
		}
		// Marked even after a failed delivery: a failing channel must not make
		// the working ones re-notify on every check.
		if err := c.opts.Store.MarkNotified(ctx, app.ID, res.LatestVersion); err != nil {
			return res, fmt.Errorf("marking notified: %w", err)
		}
	}
	return res, nil
}

func (c *Checker) appLock(id string) *sync.Mutex {
	c.mu.Lock()
	defer c.mu.Unlock()
	l, ok := c.locks[id]
	if !ok {
		l = &sync.Mutex{}
		c.locks[id] = l
	}
	return l
}
