package agenthub_test

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Wolfx56/lum/internal/agent"
	"github.com/Wolfx56/lum/internal/agentproto"
	"github.com/Wolfx56/lum/internal/config"
)

func get(t *testing.T, url string, header map[string]string) (int, http.Header, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(body)
}

func TestSelfUpdate(t *testing.T) {
	e := newEnv(t)
	newBinary := []byte("#!/bin/sh\necho new agent build\n")
	if err := os.WriteFile(filepath.Join(e.binDir, agentproto.BinaryName(runtime.GOARCH)), newBinary, 0o755); err != nil {
		t.Fatal(err)
	}
	// The running "executable" of the agent: an old build.
	exe := filepath.Join(t.TempDir(), "lum-agent")
	if err := os.WriteFile(exe, []byte("old build"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := e.startAgent("vw-lxc", testSecret, "", func(o *agent.Options) { o.Executable = exe })

	// The agent replaces its executable and stops (systemd restarts it).
	select {
	case err := <-a.done:
		if err != nil {
			t.Fatalf("Run = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the agent did not stop after its self_update")
	}
	got, _ := os.ReadFile(exe)
	if string(got) != string(newBinary) {
		t.Fatalf("executable = %q, want the server's binary", got)
	}
	if info, _ := os.Stat(exe); info.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v", info.Mode().Perm())
	}

	// Restarted with the new binary: no new self_update.
	b := e.startAgent("vw-lxc", "", a.dir, func(o *agent.Options) { o.Executable = exe })
	eventually(t, "agent reconnected", func() bool { return e.hub.Connected("vw-lxc") })
	time.Sleep(200 * time.Millisecond)
	if !e.hub.Connected("vw-lxc") {
		t.Fatal("an up-to-date agent must stay connected")
	}
	b.stop(t)
}

func TestSelfUpdateOff(t *testing.T) {
	e := newEnv(t)
	e.setConfig(func(c *config.Config) { c.Agents.SelfUpdate = config.SelfUpdateOff })
	if err := os.WriteFile(filepath.Join(e.binDir, agentproto.BinaryName(runtime.GOARCH)), []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(t.TempDir(), "lum-agent")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := e.startAgent("vw-lxc", testSecret, "", func(o *agent.Options) { o.Executable = exe })
	eventually(t, "agent connected", func() bool { return e.hub.Connected("vw-lxc") })
	time.Sleep(200 * time.Millisecond)
	if got, _ := os.ReadFile(exe); string(got) != "old" {
		t.Error("agents.self_update: off must not update the agent")
	}
	a.stop(t)
}

func TestDownloadAuthentication(t *testing.T) {
	e := newEnv(t)
	bin := []byte("agent binary")
	if err := os.WriteFile(filepath.Join(e.binDir, "lum-agent-linux-arm64"), bin, 0o755); err != nil {
		t.Fatal(err)
	}
	url := e.srv.URL + agentproto.DownloadPath + "lum-agent-linux-arm64"

	if status, _, _ := get(t, url, nil); status != http.StatusUnauthorized {
		t.Errorf("anonymous download: %d, want 401", status)
	}
	if status, _, _ := get(t, url, map[string]string{"Authorization": "Bearer wrong"}); status != http.StatusUnauthorized {
		t.Errorf("wrong secret: %d, want 401", status)
	}
	status, header, body := get(t, url, map[string]string{"Authorization": "Bearer " + testSecret})
	if status != http.StatusOK || body != string(bin) || header.Get("X-Checksum-Sha256") != agentproto.Checksum(bin) {
		t.Errorf("bootstrap secret: %d %q %v", status, body, header)
	}
	if status, _, _ := get(t, e.srv.URL+agentproto.DownloadPath+"lum-agent-linux-riscv64", map[string]string{"Authorization": "Bearer " + testSecret}); status != http.StatusNotFound {
		t.Errorf("missing arch: %d, want 404", status)
	}

	// Closed enrollment: the bootstrap secret no longer works, an agent token does.
	a := e.startAgent("vw-lxc", testSecret, "")
	eventually(t, "token saved", func() bool { _, err := os.Stat(filepath.Join(a.dir, "token")); return err == nil })
	a.stop(t)
	e.setConfig(func(c *config.Config) { c.Agents.Enrollment = config.EnrollmentClosed })
	if status, _, _ := get(t, url, map[string]string{"Authorization": "Bearer " + testSecret}); status != http.StatusUnauthorized {
		t.Errorf("secret with closed enrollment: %d, want 401", status)
	}
	token, _ := os.ReadFile(filepath.Join(a.dir, "token"))
	if status, _, _ := get(t, url, map[string]string{"Authorization": "Bearer " + strings.TrimSpace(string(token)), "X-Agent-ID": "vw-lxc"}); status != http.StatusOK {
		t.Errorf("agent token: %d, want 200", status)
	}
}

func TestInstallScript(t *testing.T) {
	e := newEnv(t)
	h := e.hub.InstallScript("https://lum.example.com/")
	srv := func(r *http.Request) string {
		w := &recorder{header: http.Header{}}
		h(w, r)
		return w.body.String()
	}
	script := srv(httptestRequest("https://whatever/install-agent.sh"))
	if !strings.Contains(script, "SERVER='https://lum.example.com'") || !strings.Contains(script, "ExecStart=/opt/lum-agent/lum-agent") ||
		strings.Contains(script, "__LUM_") {
		t.Errorf("rendered script:\n%s", script)
	}
	// Without base_url, the URL comes from the request (behind HAProxy: X-Forwarded-Proto).
	req := httptestRequest("http://lum.lan/install-agent.sh")
	req.Header.Set("X-Forwarded-Proto", "https")
	w := &recorder{header: http.Header{}}
	e.hub.InstallScript("")(w, req)
	if !strings.Contains(w.body.String(), "SERVER='https://lum.lan'") {
		t.Error("server URL must be derived from the request")
	}
	_ = context.Background()
}

type recorder struct {
	header http.Header
	body   strings.Builder
	status int
}

func (r *recorder) Header() http.Header         { return r.header }
func (r *recorder) Write(b []byte) (int, error) { return r.body.Write(b) }
func (r *recorder) WriteHeader(s int)           { r.status = s }

func httptestRequest(url string) *http.Request {
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Host = strings.Split(strings.SplitN(url, "://", 2)[1], "/")[0]
	return req
}
