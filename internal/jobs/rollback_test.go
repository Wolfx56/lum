package jobs

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wolfx56/lum/internal/config"
	"github.com/Wolfx56/lum/internal/store"
)

// failedJob runs a job that fails at the healthcheck, its snapshot kept.
func failedJob(t *testing.T, e *env) store.Job {
	t.Helper()
	e.app.pingStatus = http.StatusServiceUnavailable
	j := e.start()
	if j.Status != store.JobFailed || j.SnapshotName == "" {
		t.Fatalf("job = %+v", j)
	}
	return j
}

func TestManualRollback(t *testing.T) {
	e := newEnv(t, nil)
	j := failedJob(t, e)
	checked := e.checked.Load()
	ctx := context.Background()

	if err := e.mgr.RollbackCheck(ctx, j); err != nil {
		t.Fatalf("RollbackCheck: %v", err)
	}
	gate := make(chan struct{})
	e.pve.mu.Lock()
	e.pve.rollbackGate = gate
	e.pve.mu.Unlock()
	if e.mgr.Watch(j.ID) != nil {
		t.Error("a finished job cannot be watched")
	}
	if _, err := e.mgr.Rollback(ctx, j.ID, "bob"); err != nil {
		t.Fatal(err)
	}
	changed := e.mgr.Watch(j.ID)
	if changed == nil {
		t.Fatal("the job must be watchable during the rollback")
	}

	// Meanwhile: the job is busy, no update and no second rollback can start.
	if !e.mgr.Busy(j.ID) {
		t.Error("the job must be busy during the rollback")
	}
	if _, err := e.mgr.Start(ctx, "sonarr", "alice"); !errors.Is(err, ErrRollbackRunning) || !errors.Is(err, ErrJobActive) {
		t.Errorf("start during the rollback: err = %v", err)
	}
	if _, err := e.mgr.Rollback(ctx, j.ID, "carol"); !errors.Is(err, ErrRollbackRunning) {
		t.Errorf("second rollback: err = %v", err)
	}
	close(gate)
	select {
	case <-changed:
	case <-time.After(5 * time.Second):
		t.Fatal("watchers must be woken by the rollback")
	}
	e.mgr.wg.Wait()
	got, err := e.store.Job(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.JobRolledBack || got.Error != j.Error {
		t.Errorf("after rollback: %+v (the update error must be kept)", got)
	}
	if calls := e.pve.called("rollback"); len(calls) != 1 || calls[0] != "rollback 101 "+j.SnapshotName {
		t.Errorf("rollback calls = %v", calls)
	}
	logs := e.logs(j.ID)
	if !strings.Contains(logs, "Manual rollback requested by bob") || !strings.Contains(logs, "Rollback done") {
		t.Errorf("logs:\n%s", logs)
	}
	lines, _ := e.store.JobLogs(ctx, j.ID, 0)
	for i, l := range lines {
		if l.Seq != int64(i+1) {
			t.Fatalf("the rollback lines must follow the job lines: seq %d at %d", l.Seq, i)
		}
	}
	if e.mgr.Busy(j.ID) {
		t.Error("the job must not be busy any more")
	}
	if e.checked.Load() != checked+1 {
		t.Error("the app must be checked again after the rollback")
	}
	if err := e.mgr.RollbackCheck(ctx, got); !errors.Is(err, ErrAlreadyRolledBack) {
		t.Errorf("rolled back job: err = %v", err)
	}
}

func TestManualRollbackRefusals(t *testing.T) {
	ctx := context.Background()

	t.Run("no snapshot", func(t *testing.T) {
		e := newEnv(t, func(a *config.App) { a.SnapshotBeforeUpdate = false })
		e.app.pingStatus = http.StatusServiceUnavailable
		j := e.start()
		if _, err := e.mgr.Rollback(ctx, j.ID, "bob"); !errors.Is(err, ErrNoSnapshot) {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("snapshot deleted", func(t *testing.T) {
		e := newEnv(t, nil)
		j := failedJob(t, e)
		e.pve.mu.Lock()
		e.pve.snapshots = nil
		e.pve.mu.Unlock()
		if _, err := e.mgr.Rollback(ctx, j.ID, "bob"); !errors.Is(err, ErrSnapshotGone) {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("proxmox removed from the configuration", func(t *testing.T) {
		e := newEnv(t, nil)
		j := failedJob(t, e)
		e.mgr.opts.Proxmox = func() Snapshotter { return nil }
		if _, err := e.mgr.Rollback(ctx, j.ID, "bob"); !errors.Is(err, ErrProxmoxMissing) {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("job still running", func(t *testing.T) {
		e := newEnv(t, func(a *config.App) { a.Update.Timeout = 300 * time.Millisecond })
		e.app.upgradeTo = ""
		j, err := e.mgr.Start(ctx, "sonarr", "alice")
		if err != nil {
			t.Fatal(err)
		}
		waitStatus(t, e, j.ID, store.JobRunning)
		if _, err := e.mgr.Rollback(ctx, j.ID, "bob"); !errors.Is(err, ErrJobNotFinished) {
			t.Errorf("err = %v", err)
		}
		e.wait(j.ID)
	})

	t.Run("another job active on the app", func(t *testing.T) {
		e := newEnv(t, func(a *config.App) { a.Update.Timeout = 300 * time.Millisecond })
		old := failedJob(t, e)
		e.app.mu.Lock()
		e.app.pingStatus, e.app.version, e.app.upgradeTo = http.StatusOK, "4.0.9", ""
		e.app.mu.Unlock()
		if _, err := e.store.RecordCheck(ctx, store.Check{AppID: "sonarr", CheckedAt: time.Now(),
			CurrentVersion: "4.0.9", LatestVersion: "4.0.10", Status: "update_available"}); err != nil {
			t.Fatal(err)
		}
		j, err := e.mgr.Start(ctx, "sonarr", "alice")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.mgr.Rollback(ctx, old.ID, "bob"); !errors.Is(err, ErrJobActive) {
			t.Errorf("err = %v", err)
		}
		e.wait(j.ID)
	})

	t.Run("unknown job", func(t *testing.T) {
		e := newEnv(t, nil)
		if _, err := e.mgr.Rollback(ctx, "nope", "bob"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("err = %v", err)
		}
	})
}

func waitStatus(t *testing.T, e *env, id, status string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		j, err := e.store.Job(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if j.Status == status {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s, want %s", j.Status, status)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
