package agenthub_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wolfx56/lum/internal/agent"
	"github.com/Wolfx56/lum/internal/agenthub"
	"github.com/Wolfx56/lum/internal/agentproto"
	"github.com/Wolfx56/lum/internal/config"
	"github.com/Wolfx56/lum/internal/current"
	"github.com/Wolfx56/lum/internal/notify"
	"github.com/Wolfx56/lum/internal/scripts"
	"github.com/Wolfx56/lum/internal/store"
)

const testSecret = "test-bootstrap-secret"

type env struct {
	t          *testing.T
	cfg        atomic.Pointer[config.Config]
	store      *store.Store
	hub        *agenthub.Hub
	index      *scripts.Index
	srv        *httptest.Server
	scriptsDir string // server side
	binDir     string // agent binaries served by the hub
	now        atomic.Int64

	mu     sync.Mutex
	events []notify.Event
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, scriptsDir: t.TempDir(), binDir: t.TempDir()}
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "lum.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	e.store = db
	e.now.Store(time.Now().UnixNano())
	e.setConfig(func(*config.Config) {})
	e.index = scripts.New(e.scriptsDir, nil)
	e.hub = agenthub.New(agenthub.Options{
		Config:  func() *config.Config { return e.cfg.Load() },
		Store:   db,
		Scripts: e.index,
		Notify: func(_ context.Context, ev notify.Event) {
			e.mu.Lock()
			e.events = append(e.events, ev)
			e.mu.Unlock()
		},
		Now:      func() time.Time { return time.Unix(0, e.now.Load()) },
		AgentDir: e.binDir,
	})
	// Same routes as internal/server.
	mux := http.NewServeMux()
	mux.Handle(agentproto.Path, e.hub)
	mux.HandleFunc(agentproto.DownloadPath, e.hub.Download)
	e.srv = httptest.NewServer(mux)
	t.Cleanup(func() {
		e.hub.CloseAll()
		e.srv.Close()
	})
	return e
}

// setConfig installs a configuration: agent "vw-lxc" runs app vaultwarden with script vw.sh.
func (e *env) setConfig(edit func(*config.Config)) {
	c := &config.Config{
		Agents: config.Agents{
			SharedSecret:      testSecret,
			HeartbeatInterval: time.Second,
			OfflineAfter:      5 * time.Minute,
			Enrollment:        config.EnrollmentOpen,
			SelfUpdate:        config.SelfUpdateAuto,
		},
		Apps: []config.App{{
			ID: "vaultwarden", Agent: "vw-lxc",
			Update: &config.Update{Type: config.TypeScript, Script: "vw.sh"},
		}},
	}
	edit(c)
	e.cfg.Store(c)
}

func (e *env) writeScript(name, content string) {
	e.t.Helper()
	if err := os.WriteFile(filepath.Join(e.scriptsDir, name), []byte(content), 0o755); err != nil {
		e.t.Fatal(err)
	}
	e.index.Rescan()
}

type runningAgent struct {
	dir    string
	cancel context.CancelFunc
	done   chan error
}

func (r *runningAgent) stop(t *testing.T) {
	t.Helper()
	r.cancel()
	if err := <-r.done; err != nil {
		t.Fatalf("agent Run: %v", err)
	}
}

func (e *env) startAgent(id, secret, dir string, edit ...func(*agent.Options)) *runningAgent {
	e.t.Helper()
	if dir == "" {
		dir = e.t.TempDir()
	}
	opts := agent.Options{
		ID: id, ServerURL: e.srv.URL, Secret: secret,
		TokenFile: filepath.Join(dir, "token"), ScriptsDir: filepath.Join(dir, "scripts"),
		MinBackoff: 20 * time.Millisecond, MaxBackoff: 50 * time.Millisecond,
	}
	for _, f := range edit {
		f(&opts)
	}
	a, err := agent.New(opts)
	if err != nil {
		e.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &runningAgent{dir: dir, cancel: cancel, done: make(chan error, 1)}
	go func() { r.done <- a.Run(ctx) }()
	e.t.Cleanup(func() { cancel() })
	return r
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestEnrollmentGetVersionAndReconnect(t *testing.T) {
	e := newEnv(t)
	a := e.startAgent("vw-lxc", testSecret, "")
	eventually(t, "agent connected", func() bool { return e.hub.Connected("vw-lxc") })
	eventually(t, "token saved", func() bool { _, err := os.Stat(filepath.Join(a.dir, "token")); return err == nil })

	token, err := os.ReadFile(filepath.Join(a.dir, "token"))
	if err != nil || len(strings.TrimSpace(string(token))) < 40 {
		t.Fatalf("token file = %q, %v", token, err)
	}
	st, err := e.store.Agent(context.Background(), "vw-lxc")
	if err != nil || st.Status != store.AgentOnline || st.TokenHash == "" || strings.Contains(st.TokenHash, strings.TrimSpace(string(token))) {
		t.Fatalf("agent row = %+v, %v", st, err)
	}

	res, err := e.hub.RunCommand(context.Background(), "vw-lxc", []string{"echo", "Vaultwarden 1.34.3"})
	if err != nil || strings.TrimSpace(res.Stdout) != "Vaultwarden 1.34.3" || res.ExitCode != 0 {
		t.Fatalf("RunCommand = %+v, %v", res, err)
	}
	reader := current.NewReader(nil, e.hub)
	v, err := reader.Current(context.Background(), config.App{Agent: "vw-lxc", Current: config.Current{
		Type: config.TypeAgent, Command: []string{"echo", "Vaultwarden 1.34.3"}, OutputRegex: `(?P<version>\d+\.\d+\.\d+)`,
	}})
	if err != nil || v != "1.34.3" {
		t.Fatalf("current via agent = %q, %v", v, err)
	}
	if _, err := e.hub.RunCommand(context.Background(), "vw-lxc", []string{"/nonexistent/binary"}); err == nil {
		t.Error("a missing binary must be reported as an error")
	}

	// Restart without the secret: the saved token is enough.
	a.stop(t)
	eventually(t, "agent disconnected", func() bool { return !e.hub.Connected("vw-lxc") })
	b := e.startAgent("vw-lxc", "", a.dir)
	eventually(t, "agent reconnected with its token", func() bool { return e.hub.Connected("vw-lxc") })
	b.stop(t)
}

func TestRefusals(t *testing.T) {
	e := newEnv(t)
	cases := map[string]func(dir string) (secret string){
		"wrong secret": func(string) string { return "wrong" },
		"invalid token": func(dir string) string {
			_ = os.WriteFile(filepath.Join(dir, "token"), []byte("forged-token"), 0o600)
			return ""
		},
		"closed enrollment": func(string) string {
			e.setConfig(func(c *config.Config) { c.Agents.Enrollment = config.EnrollmentClosed })
			return testSecret
		},
	}
	for name, prepare := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			id := strings.ReplaceAll(name, " ", "-")
			a := e.startAgent(id, prepare(dir), dir)
			time.Sleep(200 * time.Millisecond)
			if e.hub.Connected(id) {
				t.Fatal("agent must be refused")
			}
			if _, err := e.store.Agent(context.Background(), id); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("refused agent must not be enrolled: %v", err)
			}
			a.stop(t)
		})
	}
}

func TestNotConnected(t *testing.T) {
	e := newEnv(t)
	_, err := e.hub.RunCommand(context.Background(), "ghost", []string{"true"})
	if !errors.Is(err, current.ErrAgentUnavailable) {
		t.Fatalf("err = %v, want ErrAgentUnavailable", err)
	}
}

func TestScriptsSync(t *testing.T) {
	e := newEnv(t)
	e.writeScript("vw.sh", "#!/usr/bin/env bash\necho v1\n")
	e.writeScript("other.sh", "#!/usr/bin/env bash\necho not for this agent\n")

	dir := t.TempDir()
	agentScripts := filepath.Join(dir, "scripts")
	if err := os.MkdirAll(agentScripts, 0o700); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(agentScripts, "stale.sh")
	if err := os.WriteFile(stale, []byte("old"), 0o700); err != nil {
		t.Fatal(err)
	}

	a := e.startAgent("vw-lxc", testSecret, dir)
	local := filepath.Join(agentScripts, "vw.sh")
	eventually(t, "script synced at connection", func() bool { return fileIs(local, "#!/usr/bin/env bash\necho v1\n") })
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("a script no longer referenced must be removed")
	}
	if _, err := os.Stat(filepath.Join(agentScripts, "other.sh")); !os.IsNotExist(err) {
		t.Error("the agent must only receive the scripts of its recipes")
	}
	eventually(t, "agent_scripts recorded", func() bool {
		got, _ := e.store.AgentScripts(context.Background(), "vw-lxc")
		s, _ := e.index.Get("vw.sh")
		return len(got) == 1 && got["vw.sh"] == s.SHA256
	})

	// Script modified on the server → pushed.
	e.writeScript("vw.sh", "#!/usr/bin/env bash\necho v2\n")
	e.hub.SyncAll(context.Background())
	eventually(t, "modified script pushed", func() bool { return fileIs(local, "#!/usr/bin/env bash\necho v2\n") })

	// Local copy tampered with → the next heartbeat reports a divergent checksum → resync.
	if err := os.WriteFile(local, []byte("tampered"), 0o700); err != nil {
		t.Fatal(err)
	}
	eventually(t, "tampered script restored", func() bool { return fileIs(local, "#!/usr/bin/env bash\necho v2\n") })

	// Recipe removed (configuration reload) → script removed from the agent.
	e.setConfig(func(c *config.Config) { c.Apps = nil })
	e.hub.SyncAll(context.Background())
	eventually(t, "unreferenced script removed", func() bool { _, err := os.Stat(local); return os.IsNotExist(err) })
	a.stop(t)
}

func TestOfflineDetection(t *testing.T) {
	e := newEnv(t)
	a := e.startAgent("vw-lxc", testSecret, "")
	eventually(t, "agent connected", func() bool { return e.hub.Connected("vw-lxc") })

	e.hub.Sweep(context.Background())
	if len(e.events) != 0 {
		t.Fatalf("no notification expected while heartbeats are fresh: %+v", e.events)
	}

	// The agent goes away; the status stays online until offline_after has
	// elapsed without heartbeat (the clock jumps ahead).
	a.stop(t)
	e.now.Add(int64(10 * time.Minute))
	e.hub.Sweep(context.Background())
	st, _ := e.store.Agent(context.Background(), "vw-lxc")
	if st.Status != store.AgentOffline {
		t.Fatalf("status = %q, want offline", st.Status)
	}
	e.mu.Lock()
	if len(e.events) != 1 || e.events[0].Type != config.EventAgentOffline || !strings.Contains(e.events[0].Title, "vw-lxc") {
		t.Fatalf("events = %+v", e.events)
	}
	e.mu.Unlock()

	// A second pass does not notify again.
	e.hub.Sweep(context.Background())
	e.mu.Lock()
	n := len(e.events)
	e.mu.Unlock()
	if n != 1 {
		t.Errorf("agent_offline notified %d times, want 1", n)
	}
}

func fileIs(path, content string) bool {
	data, err := os.ReadFile(path)
	return err == nil && string(data) == content
}
