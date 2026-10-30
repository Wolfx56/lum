package jobs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Wolfx56/lum/internal/agentproto"
	"github.com/Wolfx56/lum/internal/config"
	"github.com/Wolfx56/lum/internal/notify"
	"github.com/Wolfx56/lum/internal/proxmox"
	"github.com/Wolfx56/lum/internal/redact"
	"github.com/Wolfx56/lum/internal/semver"
	"github.com/Wolfx56/lum/internal/store"
	"github.com/Wolfx56/lum/internal/version"
)

// cleanupTimeout bounds the work done after the job deadline (rollback, retention).
const cleanupTimeout = 10 * time.Minute

// errCancelled is the cause of a job context cancelled by Cancel.
var errCancelled = errors.New("cancelled")

// run is one job being executed.
type run struct {
	m        *Manager
	app      config.App
	job      store.Job
	log      *slog.Logger
	ctx      context.Context // cancelled by Stop or Cancel
	cancel   context.CancelCauseFunc
	snapshot string // set once the snapshot exists
	// restore holds the resources to put back once the update is over (update.resources).
	restore *proxmox.Resources

	logMu sync.Mutex // the script output arrives from the hub goroutine
	seq   int64

	mu            sync.Mutex
	cancelledBy   string
	scriptRunning bool
}

// stepError is a failure with the final state it leads to.
type stepError struct {
	status string // store.JobFailed or store.JobCancelled
	err    error
}

func (e *stepError) Error() string { return e.err.Error() }

func failed(format string, args ...any) error {
	return &stepError{status: store.JobFailed, err: fmt.Errorf(format, args...)}
}

// execute runs the steps of §7.1 within update.timeout.
func (r *run) execute() {
	defer r.cancel(nil)
	ctx, cancel := context.WithTimeout(r.ctx, r.app.Update.Timeout)
	defer cancel()
	err := r.steps(ctx)

	// After the deadline (or a server stop), the remaining work gets its own time.
	cleanup, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancelCleanup()
	r.mu.Lock()
	cancelledBy := r.cancelledBy
	r.mu.Unlock()
	switch {
	case err == nil:
		r.succeed(cleanup)
	case cancelledBy != "":
		reason := "cancelled by " + cancelledBy
		r.stopScript(cleanup) // in case the script started while the job was being cancelled
		r.restoreResources(cleanup)
		r.system(cleanup, "Job cancelled by "+cancelledBy)
		if r.snapshot != "" {
			r.system(cleanup, "Snapshot "+r.snapshot+" kept: manual rollback possible")
		}
		r.finish(cleanup, store.JobCancelled, reason)
	case r.m.ctx.Err() != nil:
		// A running script is left to finish on the agent: killing an update
		// halfway is worse than its unobserved end.
		r.system(cleanup, "Server shutting down: job interrupted")
		if r.restore != nil {
			// The script may still be running: its resources are left as they are.
			r.system(cleanup, "LXC resources left raised; restore them by hand once the script is done: "+r.pctSet(*r.restore))
			r.log.Warn("container resources left raised", "lxc_id", r.app.LXCID, "restore", r.pctSet(*r.restore))
		}
		r.finish(cleanup, store.JobFailed, ReasonServerRestart)
		return
	default:
		if ctx.Err() != nil { // update.timeout reached, whatever the step was doing
			err = failed("timeout: update not finished after %s (update.timeout)", r.app.Update.Timeout)
			r.stopScript(cleanup)
		}
		r.restoreResources(cleanup)
		r.fail(cleanup, err)
	}
	if r.m.opts.Check != nil {
		r.m.opts.Check(cleanup, r.app.ID)
	}
}

func (r *run) steps(ctx context.Context) error {
	r.system(ctx, fmt.Sprintf("Updating %s: %s → %s (requested by %s)", r.app.Name, r.job.CurrentVersion, r.job.TargetVersion, r.job.CreatedBy))

	if r.app.SnapshotBeforeUpdate {
		r.status(ctx, store.JobSnapshotting)
		if err := r.takeSnapshot(ctx); err != nil {
			return &stepError{status: store.JobCancelled, err: fmt.Errorf("snapshot failed, update not started: %w", err)}
		}
	} else {
		r.system(ctx, "No snapshot (snapshot_before_update: false)")
	}

	if err := r.raiseResources(ctx); err != nil {
		return &stepError{status: store.JobCancelled, err: fmt.Errorf("resources not raised, update not started: %w", err)}
	}

	r.status(ctx, store.JobRunning)
	if r.app.Update.Type == config.TypeScript {
		if err := r.runScript(ctx); err != nil {
			return err
		}
	} else {
		if err := r.sendUpdate(ctx); err != nil {
			return err
		}
		if err := r.waitNewVersion(ctx); err != nil {
			return err
		}
	}
	// The healthcheck sees the app with the container's usual resources.
	r.restoreResources(ctx)

	if r.app.Healthcheck != nil {
		r.status(ctx, store.JobHealthchecking)
		if err := r.healthcheck(ctx); err != nil {
			return err
		}
	}
	return r.verifyVersion(ctx)
}

func (r *run) takeSnapshot(ctx context.Context) error {
	px := r.m.opts.Proxmox()
	if px == nil {
		return ErrProxmoxMissing
	}
	name := SnapshotName(r.job.ID)
	r.system(ctx, fmt.Sprintf("Taking snapshot %s of LXC %d…", name, r.app.LXCID))
	desc := fmt.Sprintf("LUM job %s: %s %s -> %s", r.job.ID, r.app.ID, r.job.CurrentVersion, r.job.TargetVersion)
	if err := px.Snapshot(ctx, r.app.LXCID, name, desc); err != nil {
		return err
	}
	r.snapshot = name
	if err := r.m.opts.Store.SetJobSnapshot(ctx, r.job.ID, name); err != nil {
		r.log.Error("snapshot name not saved", "error", err.Error())
	}
	r.system(ctx, "Snapshot created")
	return nil
}

// raiseResources gives the container at least update.resources, remembering
// the values to restore. A container already above them is left unchanged.
func (r *run) raiseResources(ctx context.Context) error {
	want := r.app.Update.Resources
	if want == nil {
		return nil
	}
	px := r.m.opts.Proxmox()
	if px == nil {
		return ErrProxmoxMissing
	}
	cur, err := px.Resources(ctx, r.app.LXCID)
	if err != nil {
		return err
	}
	var raise, before proxmox.Resources
	if want.Cores > 0 && cur.Cores > 0 && cur.Cores < want.Cores { // cores 0: all the host's cores
		raise.Cores, before.Cores = want.Cores, cur.Cores
	}
	if want.Memory > 0 && cur.Memory < want.Memory {
		raise.Memory, before.Memory = want.Memory, cur.Memory
	}
	if raise == (proxmox.Resources{}) {
		current := describeResources(cur)
		if cur.Cores == 0 {
			current = "all host CPUs, " + current
		}
		r.system(ctx, fmt.Sprintf("LXC %d resources are sufficient (%s): unchanged", r.app.LXCID, current))
		return nil
	}
	r.system(ctx, fmt.Sprintf("Raising LXC %d resources for the update: %s (instead of %s)…", r.app.LXCID, describeResources(raise), describeResources(before)))
	if err := px.SetResources(ctx, r.app.LXCID, raise); err != nil {
		return err
	}
	r.restore = &before
	r.system(ctx, "Resources raised")
	return nil
}

// restoreResources puts back the resources changed by raiseResources. A
// failure does not fail the job: the update itself is done.
func (r *run) restoreResources(ctx context.Context) {
	if r.restore == nil {
		return
	}
	before := *r.restore
	r.restore = nil
	px := r.m.opts.Proxmox()
	if px == nil {
		r.system(ctx, "Resources not restored (proxmox is no longer configured): "+r.pctSet(before))
		return
	}
	if err := px.SetResources(ctx, r.app.LXCID, before); err != nil {
		r.system(ctx, fmt.Sprintf("Resources not restored: %v. Restore them by hand: %s", err, r.pctSet(before)))
		r.log.Error("container resources not restored", "lxc_id", r.app.LXCID, "error", err.Error())
		return
	}
	r.system(ctx, fmt.Sprintf("LXC %d resources restored: %s", r.app.LXCID, describeResources(before)))
}

// pctSet is the command that restores res by hand on the Proxmox host.
func (r *run) pctSet(res proxmox.Resources) string {
	cmd := fmt.Sprintf("pct set %d", r.app.LXCID)
	if res.Cores > 0 {
		cmd += fmt.Sprintf(" --cores %d", res.Cores)
	}
	if res.Memory > 0 {
		cmd += fmt.Sprintf(" --memory %d", res.Memory)
	}
	return cmd
}

// describeResources lists the non-zero fields of res.
func describeResources(res proxmox.Resources) string {
	var parts []string
	if res.Cores > 0 {
		parts = append(parts, fmt.Sprintf("%d CPU", res.Cores))
	}
	if res.Memory > 0 {
		parts = append(parts, fmt.Sprintf("%d MiB", res.Memory))
	}
	return strings.Join(parts, ", ")
}

// runScript runs update.type script on the agent, its output going to the job logs.
func (r *run) runScript(ctx context.Context) error {
	agents := r.m.opts.Agents
	if agents == nil {
		return failed("no agent hub")
	}
	agent, script := r.app.Agent, r.app.Update.Script
	// Checked again right before running: the script may have changed since the pre-checks.
	sum, err := agents.EnsureScript(ctx, agent, script)
	if err != nil {
		return failed("%v", err)
	}
	r.system(ctx, fmt.Sprintf("Running %s on agent %s (sha256 %s)…", script, agent, sum[:12]))
	req := agentproto.RunUpdate{JobID: r.job.ID, Script: script, SHA256: sum, Env: map[string]string{
		"LUM_APP_ID":          r.app.ID,
		"LUM_APP_NAME":        r.app.Name,
		"LUM_CURRENT_VERSION": r.job.CurrentVersion,
		"LUM_TARGET_VERSION":  r.job.TargetVersion,
		"LUM_JOB_ID":          r.job.ID,
	}}
	r.mu.Lock()
	r.scriptRunning = true
	r.mu.Unlock()
	done, err := agents.RunUpdate(ctx, agent, req, func(stream, line string) { r.line(ctx, stream, line) })
	r.mu.Lock()
	r.scriptRunning = false
	r.mu.Unlock()
	switch {
	case err != nil && ctx.Err() != nil:
		return ctx.Err()
	case err != nil:
		return failed("run_update on agent %s: %v", agent, err)
	case done.Cancelled:
		return &stepError{status: store.JobCancelled, err: errCancelled}
	case done.Error != "":
		return failed("script %s: %s", script, done.Error)
	case done.ExitCode != 0:
		return failed("script %s exited with code %d", script, done.ExitCode)
	}
	r.system(ctx, "Script finished (exit code 0)")
	return nil
}

// requestCancel implements Manager.Cancel.
func (r *run) requestCancel(ctx context.Context, user string) error {
	r.mu.Lock()
	if r.cancelledBy != "" {
		r.mu.Unlock()
		return nil
	}
	r.cancelledBy = user
	script := r.scriptRunning
	r.mu.Unlock()
	r.system(ctx, "Cancellation requested by "+user)
	if script {
		// The job ends when the agent reports the end of the killed script.
		if err := r.m.opts.Agents.CancelJob(ctx, r.app.Agent, r.job.ID); err != nil {
			r.mu.Lock()
			r.cancelledBy = ""
			r.mu.Unlock()
			return fmt.Errorf("cancel_job on agent %s: %w", r.app.Agent, err)
		}
		return nil
	}
	r.cancel(errCancelled)
	return nil
}

// stopScript kills a script still running after the job deadline (best effort).
func (r *run) stopScript(ctx context.Context) {
	if r.app.Update.Type != config.TypeScript || r.m.opts.Agents == nil {
		return
	}
	if err := r.m.opts.Agents.CancelJob(ctx, r.app.Agent, r.job.ID); err == nil {
		r.system(ctx, "Script stopped on the agent (SIGTERM, then SIGKILL after 30 s)")
	}
}

// sendUpdate sends the request of update.type api; any 2xx is accepted.
func (r *run) sendUpdate(ctx context.Context) error {
	u := r.app.Update
	target := redact.URL(u.URL)
	r.system(ctx, fmt.Sprintf("Update request: %s %s", u.Method, target))
	var body io.Reader
	if u.Body != "" {
		body = strings.NewReader(u.Body)
	}
	req, err := http.NewRequestWithContext(ctx, u.Method, u.URL, body)
	if err != nil {
		return failed("update request %s: %v", target, redact.Error(err))
	}
	req.Header.Set("User-Agent", "lum/"+version.Version)
	if u.Body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if u.AuthHeader != "" {
		req.Header.Set(u.AuthHeader, u.Token.Reveal())
	}
	resp, err := r.m.opts.HTTPClient.Do(req)
	if err != nil {
		return failed("update request %s: %v", target, redact.Error(err))
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return failed("update request %s: unexpected status %d", target, resp.StatusCode)
	}
	r.system(ctx, fmt.Sprintf("Request accepted (HTTP %d), waiting for the new version…", resp.StatusCode))
	return nil
}

// waitNewVersion polls current until the installed version changes. Read
// errors are expected while the application restarts.
func (r *run) waitNewVersion(ctx context.Context) error {
	lastErr := ""
	for {
		v, err := r.m.opts.Current.Current(ctx, r.app)
		switch {
		case err != nil:
			if msg := err.Error(); msg != lastErr && ctx.Err() == nil {
				r.system(ctx, "Version not readable yet: "+msg)
				lastErr = msg
			}
		case v != r.job.CurrentVersion:
			r.system(ctx, "New version detected: "+v)
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(r.m.opts.VersionPoll):
		}
	}
}

// healthcheck polls healthcheck.url until expect_status or healthcheck.timeout.
func (r *run) healthcheck(ctx context.Context) error {
	h := r.app.Healthcheck
	target := redact.URL(h.URL)
	r.system(ctx, fmt.Sprintf("Healthcheck %s (expecting HTTP %d, %s max)…", target, h.ExpectStatus, h.Timeout))
	hctx, cancel := context.WithTimeout(ctx, h.Timeout)
	defer cancel()
	last := ""
	for {
		got, err := r.probe(hctx, h.URL)
		if err == nil && got == h.ExpectStatus {
			r.system(ctx, fmt.Sprintf("Healthcheck OK (HTTP %d)", got))
			return nil
		}
		switch {
		case err != nil && hctx.Err() != nil && last != "":
			// The probe was cut by the timeout: keep the previous, meaningful result.
		case err != nil:
			last = redact.Error(err).Error()
		default:
			last = fmt.Sprintf("HTTP %d", got)
		}
		select {
		case <-hctx.Done():
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return failed("healthcheck %s failed after %s: %s", target, h.Timeout, last)
		case <-time.After(r.m.opts.HealthPoll):
		}
	}
}

func (r *run) probe(ctx context.Context, url string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "lum/"+version.Version)
	resp, err := r.m.opts.HTTPClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, nil
}

// verifyVersion re-reads current: it must be at least the target version.
func (r *run) verifyVersion(ctx context.Context) error {
	v, err := r.m.opts.Current.Current(ctx, r.app)
	if err != nil {
		return failed("version check: %v", err)
	}
	cur, errCur := semver.Parse(v)
	want, errWant := semver.Parse(r.job.TargetVersion)
	if errCur != nil || errWant != nil || semver.Compare(cur, want) < 0 {
		return failed("installed version is %s, %s expected", v, r.job.TargetVersion)
	}
	r.system(ctx, "Version verified: "+v)
	return nil
}

func (r *run) succeed(ctx context.Context) {
	r.finish(ctx, store.JobSuccess, "")
	r.system(ctx, "Update succeeded")
	r.retention(ctx)
	r.notify(ctx, config.EventJobSuccess, r.app.Name+": update succeeded", r.job.CurrentVersion+" → "+r.job.TargetVersion)
}

// fail ends the job, with an automatic rollback if the recipe asks for it.
func (r *run) fail(ctx context.Context, err error) {
	status := store.JobFailed
	var se *stepError
	if errors.As(err, &se) {
		status = se.status
	}
	reason := err.Error()
	r.system(ctx, "Failed: "+reason)

	if status == store.JobFailed && r.snapshot != "" {
		if r.app.RollbackOnFailure {
			status, reason = r.rollback(ctx, reason)
		} else {
			r.system(ctx, "Snapshot "+r.snapshot+" kept: manual rollback possible (rollback_on_failure: false)")
		}
	}
	r.finish(ctx, status, reason)
	title := r.app.Name + ": update failed"
	if status == store.JobRolledBack {
		title = r.app.Name + ": update rolled back"
	}
	r.notify(ctx, config.EventJobFailed, title, reason)
}

func (r *run) rollback(ctx context.Context, reason string) (string, string) {
	px := r.m.opts.Proxmox()
	if px == nil {
		return store.JobFailed, reason + "; rollback not possible: proxmox is not configured"
	}
	r.system(ctx, "Automatic rollback to "+r.snapshot+"…")
	if err := px.Rollback(ctx, r.app.LXCID, r.snapshot); err != nil {
		r.system(ctx, "Rollback failed: "+err.Error())
		return store.JobFailed, reason + "; rollback failed: " + err.Error()
	}
	r.system(ctx, "Rollback done")
	return store.JobRolledBack, reason
}

// retention keeps the `retention` most recent LUM snapshots of the container.
// Failures are logged, they never fail a successful job.
func (r *run) retention(ctx context.Context) {
	px := r.m.opts.Proxmox()
	if r.snapshot == "" || px == nil {
		return
	}
	snaps, err := px.Snapshots(ctx, r.app.LXCID)
	if err != nil {
		r.system(ctx, "Snapshot retention failed: "+err.Error())
		return
	}
	var ours []string
	byName := map[string]time.Time{}
	for _, s := range snaps {
		if strings.HasPrefix(s.Name, SnapshotPrefix) {
			ours = append(ours, s.Name)
			byName[s.Name] = s.Time
		}
	}
	slices.SortFunc(ours, func(a, b string) int { return byName[b].Compare(byName[a]) }) // newest first
	for _, name := range ours[min(len(ours), r.app.Retention):] {
		if err := px.DeleteSnapshot(ctx, r.app.LXCID, name); err != nil {
			r.system(ctx, "Could not delete snapshot "+name+": "+err.Error())
			continue
		}
		r.system(ctx, fmt.Sprintf("Old snapshot %s deleted (retention: %d)", name, r.app.Retention))
	}
}

func (r *run) notify(ctx context.Context, event, title, message string) {
	if r.m.opts.Notify == nil {
		return
	}
	r.m.opts.Notify(ctx, notify.Event{
		Type: event, AppID: r.app.ID, AppName: r.app.Name,
		CurrentVersion: r.job.CurrentVersion, LatestVersion: r.job.TargetVersion,
		Title: title, Message: message,
	})
}

func (r *run) status(ctx context.Context, status string) {
	if err := r.m.opts.Store.SetJobStatus(ctx, r.job.ID, status, "", r.m.opts.Now()); err != nil {
		r.log.Error("job status not saved", "status", status, "error", err.Error())
	}
	r.m.changed(r.job.ID)
	r.log.Info("job status", "status", status)
}

func (r *run) finish(ctx context.Context, status, reason string) {
	now := r.m.opts.Now()
	if err := r.m.opts.Store.SetJobStatus(ctx, r.job.ID, status, reason, now); err != nil {
		r.log.Error("job status not saved", "status", status, "error", err.Error())
	}
	r.m.changed(r.job.ID)
	if r.m.opts.Observe != nil {
		r.m.opts.Observe(r.job.Type, status, now.Sub(r.job.CreatedAt))
	}
	if status == store.JobSuccess {
		r.log.Info("job finished", "status", status)
	} else {
		r.log.Warn("job finished", "status", status, "error", reason)
	}
}

// system writes a line of the "system" stream (LUM's own messages).
func (r *run) system(ctx context.Context, line string) { r.line(ctx, store.StreamSystem, line) }

func (r *run) line(ctx context.Context, stream, line string) {
	r.logMu.Lock()
	defer r.logMu.Unlock()
	r.seq++
	if err := r.m.opts.Store.AppendJobLog(context.WithoutCancel(ctx), r.job.ID, r.seq, stream, line, r.m.opts.Now()); err != nil {
		r.log.Error("job log not saved", "error", err.Error())
	}
	r.m.changed(r.job.ID)
	r.log.Debug("job log", "line", line)
}
