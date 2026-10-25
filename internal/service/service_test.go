package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Wolfx56/lum/internal/config"
	"github.com/Wolfx56/lum/internal/store"
)

const apps = "apps: []\n"

func newService(t *testing.T, serverYAML string) (*Service, string) {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, "server.yaml", serverYAML)
	write(t, dir, "apps.yaml", apps)
	holder, err := config.NewHolder(config.Options{Dir: dir, LookupEnv: func(string) (string, bool) { return "", false }})
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "lum.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	svc, err := New(Options{Holder: holder, Store: st})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Stop)
	return svc, dir
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReloadRebuildsOnlyWhatChanged(t *testing.T) {
	svc, dir := newService(t, "auth: {mode: none}\nscheduler: {check_cron: \"0 7 * * *\"}\n")
	gh, sched := svc.github.Load(), svc.sched

	// Unrelated change: the GitHub client (and its cache) and the scheduler are kept.
	write(t, dir, "server.yaml", "auth: {mode: none}\nscheduler: {check_cron: \"0 7 * * *\", concurrency: 3}\n")
	if err := svc.Reload(); err != nil {
		t.Fatal(err)
	}
	if svc.github.Load() != gh || svc.sched != sched {
		t.Error("components must be kept when their settings did not change")
	}

	// New cron and cache TTL: both are rebuilt.
	write(t, dir, "server.yaml", "auth: {mode: none}\nscheduler: {check_cron: \"0 8 * * *\"}\ngithub: {cache_ttl: 1m}\n")
	if err := svc.Reload(); err != nil {
		t.Fatal(err)
	}
	if svc.github.Load() == gh || svc.sched == sched {
		t.Error("GitHub client and scheduler must be rebuilt")
	}
	if h := svc.NextCheck().Hour(); h != 8 {
		t.Errorf("next check at %dh, want 8h", h)
	}
}

func TestFailedReloadKeepsEverything(t *testing.T) {
	svc, dir := newService(t, "auth: {mode: none}\n")
	sched := svc.sched
	write(t, dir, "server.yaml", "auth: {mode: none}\nscheduler: {check_cron: \"not a cron\"}\n")
	var verr *config.ValidationError
	if err := svc.Reload(); !errors.As(err, &verr) {
		t.Fatalf("want a validation error, got %v", err)
	}
	if svc.sched != sched || svc.Config().Scheduler.CheckCron != config.DefaultCheckCron {
		t.Error("a failed reload must not change anything")
	}
}

func TestCheckAllIsExclusive(t *testing.T) {
	svc, _ := newService(t, "auth: {mode: none}\n")
	svc.fullCheck.Store(true) // simulate a running check
	if _, err := svc.CheckAll(context.Background()); !errors.Is(err, ErrCheckRunning) {
		t.Fatalf("want ErrCheckRunning, got %v", err)
	}
	svc.fullCheck.Store(false)
	if _, err := svc.CheckAll(context.Background()); err != nil {
		t.Fatal(err)
	}
}
