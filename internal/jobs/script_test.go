package jobs_test

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wolfx56/lum/internal/agent"
	"github.com/Wolfx56/lum/internal/agenthub"
	"github.com/Wolfx56/lum/internal/config"
	"github.com/Wolfx56/lum/internal/current"
	"github.com/Wolfx56/lum/internal/jobs"
	"github.com/Wolfx56/lum/internal/scripts"
	"github.com/Wolfx56/lum/internal/store"
)

// scriptEnv runs a real hub, a real agent and the job manager. The "installed
// version" is a file of the agent's directory, read with cat (current.type
// agent), and written by the update script.
type scriptEnv struct {
	t           *testing.T
	store       *store.Store
	hub         *agenthub.Hub
	mgr         *jobs.Manager
	index       *scripts.Index
	srv         *httptest.Server
	cfg         atomic.Pointer[config.Config]
	scriptsDir  string
	versionFile string
	agentDir    string
	stopAgent   context.CancelFunc
	agentDone   chan error
}

func newScriptEnv(t *testing.T, script string) *scriptEnv {
	t.Helper()
	e := &scriptEnv{t: t, scriptsDir: t.TempDir(), agentDir: t.TempDir()}
	e.versionFile = filepath.Join(e.agentDir, "version")
	if err := os.WriteFile(e.versionFile, []byte("app 1.0.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.scriptsDir, "up.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "lum.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	e.store = db
	e.cfg.Store(&config.Config{
		Agents: config.Agents{SharedSecret: "test-secret", HeartbeatInterval: time.Minute, OfflineAfter: 5 * time.Minute, Enrollment: config.EnrollmentOpen},
		Apps: []config.App{{
			ID: "vaultwarden", Name: "Vaultwarden", LXCID: 102, Agent: "vw-lxc", Retention: 2,
			Current: config.Current{Type: config.TypeAgent, Command: []string{"cat", e.versionFile}, OutputRegex: `(?P<version>\d+\.\d+\.\d+)`},
			Update:  &config.Update{Type: config.TypeScript, Script: "up.sh", Timeout: 10 * time.Second},
		}},
	})
	cfg := func() *config.Config { return e.cfg.Load() }
	e.index = scripts.New(e.scriptsDir, nil)
	e.hub = agenthub.New(agenthub.Options{Config: cfg, Store: db, Scripts: e.index})
	e.srv = httptest.NewServer(e.hub)
	t.Cleanup(e.srv.Close)
	t.Cleanup(e.hub.CloseAll)

	e.mgr = jobs.New(jobs.Options{Config: cfg, Store: db, Current: current.NewReader(nil, e.hub), Agents: e.hub})
	t.Cleanup(e.mgr.Stop)

	if _, err := db.RecordCheck(context.Background(), store.Check{AppID: "vaultwarden", CheckedAt: time.Now(),
		CurrentVersion: "1.0.0", LatestVersion: "1.1.0", Status: "update_available"}); err != nil {
		t.Fatal(err)
	}
	e.startAgent()
	return e
}

func (e *scriptEnv) startAgent() {
	e.t.Helper()
	a, err := agent.New(agent.Options{
		ID: "vw-lxc", ServerURL: e.srv.URL, Secret: "test-secret",
		TokenFile: filepath.Join(e.agentDir, "token"), ScriptsDir: filepath.Join(e.agentDir, "scripts"),
		MinBackoff: 20 * time.Millisecond, MaxBackoff: 50 * time.Millisecond,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.stopAgent, e.agentDone = cancel, make(chan error, 1)
	go func() { e.agentDone <- a.Run(ctx) }()
	e.t.Cleanup(cancel)
	waitFor(e.t, "agent connected", func() bool { return e.hub.Connected("vw-lxc") })
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (e *scriptEnv) job(id string) store.Job {
	j, err := e.store.Job(context.Background(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	return j
}

func (e *scriptEnv) waitEnd(id string) store.Job {
	e.t.Helper()
	waitFor(e.t, "job end", func() bool { return !slices.Contains(store.ActiveJobStates, e.job(id).Status) })
	return e.job(id)
}

func (e *scriptEnv) logs(id string) []store.JobLog {
	logs, err := e.store.JobLogs(context.Background(), id, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	return logs
}

func hasLine(logs []store.JobLog, stream, line string) bool {
	return slices.ContainsFunc(logs, func(l store.JobLog) bool { return l.Stream == stream && l.Line == line })
}

const upgradeScript = `#!/usr/bin/env bash
set -euo pipefail
echo "update $LUM_APP_ID ($LUM_APP_NAME) $LUM_CURRENT_VERSION -> $LUM_TARGET_VERSION, job $LUM_JOB_ID"
printf '\033[32mcolored\033[0m\n'
printf 'progress 10%%\rprogress 100%%\n'
echo "a warning" >&2
echo "app $LUM_TARGET_VERSION" > "VERSION_FILE"
`

func TestScriptJobSuccess(t *testing.T) {
	e := newScriptEnv(t, "")
	script := strings.ReplaceAll(upgradeScript, "VERSION_FILE", e.versionFile)
	if err := os.WriteFile(filepath.Join(e.scriptsDir, "up.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	e.index.Rescan() // modified after the agent connected: EnsureScript must push it

	j, err := e.mgr.Start(context.Background(), "vaultwarden", "alice")
	if err != nil {
		t.Fatal(err)
	}
	j = e.waitEnd(j.ID)
	logs := e.logs(j.ID)
	if j.Status != store.JobSuccess {
		t.Fatalf("job = %+v\nlogs: %+v", j, logs)
	}
	for _, want := range []struct{ stream, line string }{
		{"stdout", "update vaultwarden (Vaultwarden) 1.0.0 -> 1.1.0, job " + j.ID},
		{"stdout", "colored"},       // ANSI sequences removed
		{"stdout", "progress 100%"}, // carriage return resolved
		{"stderr", "a warning"},
		{"system", "Script finished (exit code 0)"},
		{"system", "Version verified: 1.1.0"},
	} {
		if !hasLine(logs, want.stream, want.line) {
			t.Errorf("missing %s line %q in %+v", want.stream, want.line, logs)
		}
	}
}

func TestScriptJobFailure(t *testing.T) {
	e := newScriptEnv(t, "#!/usr/bin/env bash\necho 'cargo build failed' >&2\nexit 3\n")
	j, err := e.mgr.Start(context.Background(), "vaultwarden", "alice")
	if err != nil {
		t.Fatal(err)
	}
	j = e.waitEnd(j.ID)
	if j.Status != store.JobFailed || j.Error != "script up.sh exited with code 3" {
		t.Fatalf("job = %+v", j)
	}
	if !hasLine(e.logs(j.ID), "stderr", "cargo build failed") {
		t.Error("the script output must be in the job logs")
	}
}

func TestScriptJobCancel(t *testing.T) {
	e := newScriptEnv(t, "#!/usr/bin/env bash\necho started\nsleep 30\necho never\n")
	j, err := e.mgr.Start(context.Background(), "vaultwarden", "alice")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "script started", func() bool { return hasLine(e.logs(j.ID), "stdout", "started") })
	start := time.Now()
	if err := e.mgr.Cancel(context.Background(), j.ID, "bob"); err != nil {
		t.Fatal(err)
	}
	j = e.waitEnd(j.ID)
	if j.Status != store.JobCancelled || j.Error != "cancelled by bob" {
		t.Fatalf("job = %+v", j)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("SIGTERM must stop the script (and its children) without waiting for SIGKILL")
	}
	if hasLine(e.logs(j.ID), "stdout", "never") {
		t.Error("the script must have been stopped")
	}
}

func TestScriptJobSurvivesAgentReconnection(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "go")
	e := newScriptEnv(t, "#!/usr/bin/env bash\necho before\nwhile [ ! -e "+marker+" ]; do sleep 0.05; done\necho after\nexit 4\n")
	j, err := e.mgr.Start(context.Background(), "vaultwarden", "alice")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "script started", func() bool { return hasLine(e.logs(j.ID), "stdout", "before") })

	// The server drops the connection; the script goes on, its output is kept
	// by the agent and delivered once reconnected.
	e.hub.CloseAll()
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	j = e.waitEnd(j.ID)
	if j.Status != store.JobFailed || !strings.Contains(j.Error, "code 4") || !hasLine(e.logs(j.ID), "stdout", "after") {
		t.Fatalf("job = %+v, logs = %+v", j, e.logs(j.ID))
	}
}

func TestScriptJobRefusedWhenAgentOffline(t *testing.T) {
	e := newScriptEnv(t, "#!/usr/bin/env bash\ntrue\n")
	e.stopAgent()
	if err := <-e.agentDone; err != nil {
		t.Fatal(err)
	}
	waitFor(t, "agent disconnected", func() bool { return !e.hub.Connected("vw-lxc") })
	_, err := e.mgr.Start(context.Background(), "vaultwarden", "alice")
	if err == nil || !strings.Contains(err.Error(), "agent offline") {
		t.Fatalf("err = %v, want agent offline", err)
	}
	if jobs, _ := e.store.Jobs(context.Background(), "vaultwarden", 10); len(jobs) != 0 {
		t.Error("no job must be created (spec §7.4: refused before any snapshot)")
	}
}
