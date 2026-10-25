// Package scheduler runs the periodic jobs of the server with an internal cron
// (no system cron, spec §13): version checks on scheduler.check_cron with a
// random jitter, and the daily purge of the check history.
// See doc/checks-notifications.md.
package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/Wolfx56/lum/internal/store"
)

// Retention of version_checks (spec §12).
const Retention = 90 * 24 * time.Hour

// PurgeCron is when the history purge runs (daily, at night).
const PurgeCron = "30 3 * * *"

// Runner checks every app (checker.Checker).
type Runner interface {
	CheckAll(ctx context.Context) []store.Check
}

// Purger deletes old history and expired sessions (store.Store).
type Purger interface {
	PurgeChecks(ctx context.Context, before time.Time) (int64, error)
	PurgeJobLogs(ctx context.Context, before time.Time) (int64, error)
	PurgeSessions(ctx context.Context, now time.Time) (int64, error)
}

// Options configures the scheduler.
type Options struct {
	CheckCron string
	Jitter    time.Duration
	Runner    Runner
	Purger    Purger
	Logger    *slog.Logger
}

// Scheduler owns the cron.
type Scheduler struct {
	cron    *cron.Cron
	checkID cron.EntryID
	cancel  context.CancelFunc
}

// Start schedules the jobs. A check never overlaps the previous one.
func Start(opts Options) (*Scheduler, error) {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Scheduler{
		cron:   cron.New(cron.WithChain(cron.SkipIfStillRunning(cron.DiscardLogger))),
		cancel: cancel,
	}
	log := opts.Logger

	var err error
	s.checkID, err = s.cron.AddFunc(opts.CheckCron, func() {
		if d := jitter(opts.Jitter); d > 0 {
			log.Debug("scheduled check delayed by jitter", "delay", d.String())
			select {
			case <-time.After(d):
			case <-ctx.Done():
				return
			}
		}
		log.Info("scheduled check started")
		opts.Runner.CheckAll(ctx)
	})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("scheduler.check_cron: %w", err)
	}
	if _, err := s.cron.AddFunc(PurgeCron, func() { Purge(ctx, opts.Purger, log) }); err != nil {
		cancel()
		return nil, err
	}

	s.cron.Start()
	log.Info("scheduler started", "check_cron", opts.CheckCron, "jitter", opts.Jitter.String(), "next_check", s.NextCheck().Format(time.RFC3339))
	return s, nil
}

// NextCheck returns the time of the next scheduled check.
func (s *Scheduler) NextCheck() time.Time {
	// Computed from the schedule: Entry.Next is only filled once the cron loop has run.
	return s.cron.Entry(s.checkID).Schedule.Next(time.Now())
}

// Stop cancels running jobs and waits for them to return.
func (s *Scheduler) Stop() {
	s.cancel()
	<-s.cron.Stop().Done()
}

// Purge deletes the checks and job logs older than Retention and the expired sessions.
func Purge(ctx context.Context, p Purger, log *slog.Logger) {
	now := time.Now()
	checks, err := p.PurgeChecks(ctx, now.Add(-Retention))
	if err != nil {
		log.Error("history purge failed", "error", err.Error())
		return
	}
	logs, err := p.PurgeJobLogs(ctx, now.Add(-Retention))
	if err != nil {
		log.Error("job logs purge failed", "error", err.Error())
		return
	}
	sessions, err := p.PurgeSessions(ctx, now)
	if err != nil {
		log.Error("session purge failed", "error", err.Error())
		return
	}
	log.Info("history purged", "deleted_checks", checks, "deleted_job_logs", logs, "expired_sessions", sessions)
}

// jitter returns a random delay in [0, max).
func jitter(limit time.Duration) time.Duration {
	if limit <= 0 {
		return 0
	}
	return rand.N(limit)
}
