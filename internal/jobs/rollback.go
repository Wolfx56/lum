package jobs

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/Wolfx56/lum/internal/config"
	"github.com/Wolfx56/lum/internal/proxmox"
	"github.com/Wolfx56/lum/internal/store"
)

// RollbackCheck tells whether the job can be rolled back by hand, with the
// refusal Rollback would return (ErrNoSnapshot, ErrJobNotFinished,
// ErrAlreadyRolledBack, ErrUnknownApp, ErrProxmoxMissing, ErrJobActive,
// ErrSnapshotGone, or a Proxmox error).
func (m *Manager) RollbackCheck(ctx context.Context, job store.Job) error {
	_, err := m.rollbackTarget(ctx, job)
	return err
}

func (m *Manager) rollbackTarget(ctx context.Context, job store.Job) (config.App, error) {
	switch {
	case job.SnapshotName == "":
		return config.App{}, ErrNoSnapshot
	case slices.Contains(store.ActiveJobStates, job.Status):
		return config.App{}, ErrJobNotFinished
	case job.Status == store.JobRolledBack:
		return config.App{}, ErrAlreadyRolledBack
	}
	app, ok := m.opts.Config().App(job.AppID)
	if !ok {
		return config.App{}, fmt.Errorf("%w: %s", ErrUnknownApp, job.AppID)
	}
	px := m.opts.Proxmox()
	if px == nil {
		return config.App{}, ErrProxmoxMissing
	}
	m.mu.Lock()
	_, rollingBack := m.rollbacks[app.ID]
	m.mu.Unlock()
	if rollingBack {
		return config.App{}, fmt.Errorf("%w: %w", ErrJobActive, ErrRollbackRunning)
	}
	active, err := m.opts.Store.ActiveJobs(ctx)
	if err != nil {
		return config.App{}, err
	}
	if a, ok := active[app.ID]; ok {
		return config.App{}, fmt.Errorf("%w: job %s", ErrJobActive, a.ID)
	}
	// Retention may have deleted it since.
	snaps, err := px.Snapshots(ctx, app.LXCID)
	if err != nil {
		return config.App{}, fmt.Errorf("%w: listing the snapshots of LXC %d: %v", ErrProxmox, app.LXCID, err)
	}
	if !slices.ContainsFunc(snaps, func(s proxmox.Snapshot) bool { return s.Name == job.SnapshotName }) {
		return config.App{}, fmt.Errorf("%w: %s on LXC %d", ErrSnapshotGone, job.SnapshotName, app.LXCID)
	}
	return app, nil
}

// Rollback restores the snapshot of a finished job in the background (manual
// rollback, spec §7.1). The whole container goes back to the snapshot. The
// job ends rolled_back; its logs tell how the rollback went. No update can
// start on the app meanwhile.
func (m *Manager) Rollback(ctx context.Context, jobID, user string) (store.Job, error) {
	job, err := m.opts.Store.Job(ctx, jobID)
	if err != nil {
		return store.Job{}, err
	}
	app, err := m.rollbackTarget(ctx, job)
	if err != nil {
		return store.Job{}, err
	}
	m.mu.Lock()
	if _, ok := m.rollbacks[app.ID]; ok {
		m.mu.Unlock()
		return store.Job{}, fmt.Errorf("%w: %w", ErrJobActive, ErrRollbackRunning)
	}
	m.rollbacks[app.ID] = job.ID
	m.mu.Unlock()

	r := &run{m: m, app: app, job: job, log: m.opts.Logger.With("job_id", job.ID, "app_id", app.ID)}
	r.seq, err = m.opts.Store.LastJobLogSeq(ctx, job.ID)
	if err != nil {
		m.endRollback(app.ID)
		return store.Job{}, err
	}
	r.log.Info("manual rollback", "user", user, "snapshot", job.SnapshotName)
	m.wg.Go(func() {
		defer func() {
			m.endRollback(app.ID)
			m.changed(job.ID) // after: watchers see a job that is no longer busy
		}()
		ctx, cancel := context.WithTimeout(context.WithoutCancel(m.ctx), cleanupTimeout)
		defer cancel()
		r.manualRollback(ctx, user)
		if m.opts.Check != nil {
			m.opts.Check(ctx, app.ID)
		}
	})
	return job, nil
}

func (m *Manager) endRollback(appID string) {
	m.mu.Lock()
	delete(m.rollbacks, appID)
	m.mu.Unlock()
}

func (r *run) manualRollback(ctx context.Context, user string) {
	r.system(ctx, fmt.Sprintf("Manual rollback requested by %s: restoring LXC %d to snapshot %s…", user, r.app.LXCID, r.job.SnapshotName))
	px := r.m.opts.Proxmox()
	if px == nil {
		r.system(ctx, "Rollback not possible: "+ErrProxmoxMissing.Error())
		return
	}
	if err := px.Rollback(ctx, r.app.LXCID, r.job.SnapshotName); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			err = fmt.Errorf("not finished after %s", cleanupTimeout)
		}
		r.system(ctx, "Rollback failed: "+err.Error())
		r.log.Error("manual rollback failed", "error", err.Error())
		return
	}
	// The error of the update is kept: it says why the rollback was needed.
	if err := r.m.opts.Store.SetJobStatus(ctx, r.job.ID, store.JobRolledBack, r.job.Error, r.m.opts.Now()); err != nil {
		r.log.Error("job status not saved", "status", store.JobRolledBack, "error", err.Error())
	}
	r.system(ctx, "Rollback done: the container is back to its pre-update state")
	r.log.Info("manual rollback done")
}
