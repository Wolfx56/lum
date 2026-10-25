package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Wolfx56/lum/internal/agentproto"
)

func TestWebsocketURL(t *testing.T) {
	ok := map[string]string{
		"https://lum.example.com":      "wss://lum.example.com/agent/ws",
		"https://lum.example.com/lum/": "wss://lum.example.com/lum/agent/ws",
		"http://localhost:8080":        "ws://localhost:8080/agent/ws",
		"http://127.0.0.1:8080":        "ws://127.0.0.1:8080/agent/ws",
		"http://[::1]:8080":            "ws://[::1]:8080/agent/ws",
	}
	for in, want := range ok {
		if got, err := websocketURL(in); err != nil || got != want {
			t.Errorf("websocketURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	// TLS is mandatory outside localhost (spec §10).
	for _, in := range []string{"http://lum.example.com", "http://192.0.2.10:8080", "ftp://x", "", "lum.example.com"} {
		if _, err := websocketURL(in); err == nil {
			t.Errorf("websocketURL(%q): expected an error", in)
		}
	}
}

func newTestAgent(t *testing.T) *Agent {
	t.Helper()
	dir := t.TempDir()
	a, err := New(Options{ID: "a", ServerURL: "http://localhost:1", TokenFile: filepath.Join(dir, "token"), ScriptsDir: filepath.Join(dir, "scripts")})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestRunNeedsTokenOrSecret(t *testing.T) {
	if err := newTestAgent(t).Run(context.Background()); err == nil || !strings.Contains(err.Error(), "cannot enroll") {
		t.Fatalf("Run = %v, want a 'cannot enroll' error", err)
	}
}

func script(name, content string) agentproto.Script {
	return agentproto.Script{Name: name, Content: []byte(content), SHA256: agentproto.Checksum([]byte(content)), Mode: 0o644}
}

func TestSyncScriptsRejectsInvalidOrdersWithoutWriting(t *testing.T) {
	a := newTestAgent(t)
	bad := script("b.sh", "x")
	bad.SHA256 = agentproto.Checksum([]byte("other"))
	for name, req := range map[string]agentproto.SyncScripts{
		"checksum":   {Scripts: []agentproto.Script{script("a.sh", "ok"), bad}},
		"path":       {Scripts: []agentproto.Script{script("../evil.sh", "x")}},
		"hidden":     {Scripts: []agentproto.Script{script(".x.sh", "x")}},
		"not a .sh":  {Scripts: []agentproto.Script{script("run", "x")}},
		"bare .sh":   {Scripts: []agentproto.Script{script(".sh", "x")}},
		"subdir":     {Scripts: []agentproto.Script{script("d/a.sh", "x")}},
		"backslash":  {Scripts: []agentproto.Script{script(`d\a.sh`, "x")}},
		"empty name": {Scripts: []agentproto.Script{script("", "x")}},
	} {
		if _, err := a.syncScripts(req); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if entries, _ := os.ReadDir(a.opts.ScriptsDir); len(entries) != 0 {
		t.Errorf("nothing must be written on a refused sync, got %d entries", len(entries))
	}
}

func TestSyncScriptsWritesAndPrunes(t *testing.T) {
	a := newTestAgent(t)
	if _, err := a.syncScripts(agentproto.SyncScripts{Scripts: []agentproto.Script{script("a.sh", "A"), script("b.sh", "B")}}); err != nil {
		t.Fatal(err)
	}
	res, err := a.syncScripts(agentproto.SyncScripts{Scripts: []agentproto.Script{script("a.sh", "A2")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Scripts) != 1 || res.Scripts["a.sh"] != agentproto.Checksum([]byte("A2")) {
		t.Fatalf("result = %+v", res.Scripts)
	}
	info, err := os.Stat(filepath.Join(a.opts.ScriptsDir, "a.sh"))
	if err != nil || info.Mode().Perm() != 0o744 {
		t.Errorf("a.sh mode = %v, %v; want 0744 (owner rwx added, others at most r-x)", info.Mode().Perm(), err)
	}
	if _, err := os.Stat(filepath.Join(a.opts.ScriptsDir, "b.sh")); !os.IsNotExist(err) {
		t.Error("b.sh must be removed")
	}
}

func TestGetVersion(t *testing.T) {
	a := newTestAgent(t)
	res, err := a.getVersion(context.Background(), agentproto.GetVersion{Command: []string{"sh", "-c", "echo out; echo err >&2; exit 3"}})
	if err != nil || res.Stdout != "out\n" || res.Stderr != "err\n" || res.ExitCode != 3 {
		t.Fatalf("getVersion = %+v, %v", res, err)
	}
	// No shell: arguments are passed as is.
	res, err = a.getVersion(context.Background(), agentproto.GetVersion{Command: []string{"echo", "$HOME;", "x"}})
	if err != nil || res.Stdout != "$HOME; x\n" {
		t.Fatalf("getVersion = %+v, %v", res, err)
	}
	if _, err := a.getVersion(context.Background(), agentproto.GetVersion{}); err == nil {
		t.Error("an empty command must be refused")
	}
	res, err = a.getVersion(context.Background(), agentproto.GetVersion{Command: []string{"sh", "-c", "yes | head -c 200000"}})
	if err != nil || len(res.Stdout) != maxCommandOutput {
		t.Errorf("output must be capped at %d bytes, got %d (%v)", maxCommandOutput, len(res.Stdout), err)
	}
}

// Scripts run in "/": a relative scripts directory (make run-agent) must be
// made absolute, or the script path no longer resolves.
func TestRelativeScriptsDirIsMadeAbsolute(t *testing.T) {
	a, err := New(Options{ID: "x", ServerURL: "http://localhost:8080", TokenFile: "token", ScriptsDir: "tmp/scripts"})
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(a.opts.ScriptsDir) {
		t.Errorf("ScriptsDir = %q, want an absolute path", a.opts.ScriptsDir)
	}
}
