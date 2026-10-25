package proxmox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wolfx56/lum/internal/config"
)

const testToken = "lum@pve!lum"
const testSecret = "test-proxmox-secret"

// fakePVE simulates the parts of the Proxmox API used by LUM.
type fakePVE struct {
	mu        sync.Mutex
	running   bool
	snapshots map[string]string // name → description
	failTask  string            // exit status of the next task, if set
	config    map[string]any    // configuration of LXC 101
	calls     []string
	polls     int
}

func (f *fakePVE) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "PVEAPIToken="+testToken+"="+testSecret {
		http.Error(w, "authentication failure", http.StatusUnauthorized)
		return
	}
	_ = r.ParseForm()
	p := strings.TrimPrefix(r.URL.Path, "/api2/json")
	f.calls = append(f.calls, r.Method+" "+p)
	reply := func(data any) { _ = json.NewEncoder(w).Encode(map[string]any{"data": data}) }
	task := func() {
		reply("UPID:pve1:0001:task:" + strings.ReplaceAll(p, "/", "_"))
	}
	switch {
	case p == "/version":
		reply(map[string]string{"version": "8.2.4"})
	case p == "/cluster/resources":
		reply([]map[string]any{{"vmid": 101, "node": "pve1", "type": "lxc"}, {"vmid": 200, "node": "pve1", "type": "qemu"}})
	case p == "/nodes/pve1/lxc/101/snapshot" && r.Method == http.MethodGet:
		list := []map[string]any{{"name": "current"}}
		for name, desc := range f.snapshots {
			list = append(list, map[string]any{"name": name, "description": desc, "snaptime": 1760000000})
		}
		reply(list)
	case p == "/nodes/pve1/lxc/101/snapshot" && r.Method == http.MethodPost:
		name := r.PostForm.Get("snapname")
		if len(name) > 40 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"data":null,"errors":{"snapname":"value may only be 40 characters long"}}`))
			return
		}
		f.snapshots[name] = r.PostForm.Get("description")
		task()
	case strings.HasPrefix(p, "/nodes/pve1/lxc/101/snapshot/") && strings.HasSuffix(p, "/rollback"):
		f.running = false // Proxmox stops the container to roll it back
		task()
	case strings.HasPrefix(p, "/nodes/pve1/lxc/101/snapshot/") && r.Method == http.MethodDelete:
		delete(f.snapshots, strings.TrimPrefix(p, "/nodes/pve1/lxc/101/snapshot/"))
		task()
	case p == "/nodes/pve1/lxc/101/config" && r.Method == http.MethodGet:
		reply(f.config)
	case p == "/nodes/pve1/lxc/101/config" && r.Method == http.MethodPut:
		for k := range r.PostForm {
			f.config[k] = r.PostForm.Get(k)
		}
		reply(nil)
	case p == "/nodes/pve1/lxc/101/status/current":
		status := "stopped"
		if f.running {
			status = "running"
		}
		reply(map[string]string{"status": status})
	case p == "/nodes/pve1/lxc/101/status/start":
		f.running = true
		task()
	case strings.HasPrefix(p, "/nodes/pve1/tasks/"):
		f.polls++
		if f.polls%2 == 1 { // every task runs for one poll before stopping
			reply(map[string]string{"status": "running"})
			return
		}
		exit := "OK"
		if f.failTask != "" {
			exit, f.failTask = f.failTask, ""
		}
		reply(map[string]string{"status": "stopped", "exitstatus": exit})
	default:
		http.Error(w, "no such path", http.StatusNotImplemented)
	}
}

func newFake(t *testing.T) (*fakePVE, *httptest.Server, string) {
	t.Helper()
	taskPoll = time.Millisecond
	f := &fakePVE{snapshots: map[string]string{}, config: map[string]any{"hostname": "sonarr"}, running: true}
	srv := httptest.NewUnstartedServer(f)
	srv.Config.ErrorLog = log.New(io.Discard, "", 0) // refused handshakes are expected
	srv.StartTLS()
	t.Cleanup(srv.Close)
	sum := sha256.Sum256(srv.Certificate().Raw)
	return f, srv, formatFingerprint(hex.EncodeToString(sum[:]))
}

func client(url, fingerprint string) *Client {
	return New(config.Proxmox{URL: url, TokenID: testToken, TokenSecret: testSecret, Fingerprint: fingerprint})
}

func TestFingerprintPinning(t *testing.T) {
	_, srv, fp := newFake(t)
	if v, err := client(srv.URL, fp).Version(context.Background()); err != nil || v != "8.2.4" {
		t.Fatalf("pinned fingerprint: %q, %v", v, err)
	}
	wrong := "00" + fp[2:]
	if _, err := client(srv.URL, wrong).Version(context.Background()); err == nil || !strings.Contains(err.Error(), "fingerprint mismatch") {
		t.Errorf("wrong fingerprint: err = %v", err)
	}
	// Without a fingerprint the system CAs apply: the self-signed certificate is refused.
	if _, err := client(srv.URL, "").Version(context.Background()); err == nil {
		t.Error("self-signed certificate must be refused without a fingerprint")
	}
}

func TestErrorsNeverContainTheToken(t *testing.T) {
	_, srv, fp := newFake(t)
	c := New(config.Proxmox{URL: srv.URL, TokenID: testToken, TokenSecret: "test-wrong-secret", Fingerprint: fp})
	_, err := c.Version(context.Background())
	if err == nil || !strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "test-wrong-secret") {
		t.Errorf("err = %v", err)
	}
}

func TestSnapshotLifecycle(t *testing.T) {
	f, srv, fp := newFake(t)
	c := client(srv.URL, fp)
	ctx := context.Background()

	if err := c.Snapshot(ctx, 101, "lum-pre-update-0123456789ab", "LUM job x"); err != nil {
		t.Fatal(err)
	}
	snaps, err := c.Snapshots(ctx, 101)
	if err != nil || len(snaps) != 1 || snaps[0].Name != "lum-pre-update-0123456789ab" || snaps[0].Description != "LUM job x" {
		t.Fatalf("snapshots = %+v, %v", snaps, err)
	}

	// Rollback of a running container: rolled back, then started again.
	if err := c.Rollback(ctx, 101, "lum-pre-update-0123456789ab"); err != nil {
		t.Fatal(err)
	}
	if !f.running || !slices.Contains(f.calls, "POST /nodes/pve1/lxc/101/status/start") {
		t.Errorf("container must be restarted after rollback; calls = %v", f.calls)
	}

	if err := c.DeleteSnapshot(ctx, 101, "lum-pre-update-0123456789ab"); err != nil {
		t.Fatal(err)
	}
	if snaps, _ := c.Snapshots(ctx, 101); len(snaps) != 0 {
		t.Errorf("snapshot must be deleted: %+v", snaps)
	}
}

func TestFailures(t *testing.T) {
	f, srv, fp := newFake(t)
	c := client(srv.URL, fp)
	ctx := context.Background()

	f.failTask = "snapshot feature is not available"
	err := c.Snapshot(ctx, 101, "lum-a", "")
	if err == nil || !strings.Contains(err.Error(), "proxmox task failed: snapshot feature is not available") {
		t.Errorf("failed task: err = %v", err)
	}
	err = c.Snapshot(ctx, 101, strings.Repeat("x", 41), "")
	if err == nil || !strings.Contains(err.Error(), "snapname: value may only be 40 characters long") {
		t.Errorf("parameter error: err = %v", err)
	}
	if err := c.Snapshot(ctx, 999, "lum-a", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown LXC: err = %v, want ErrNotFound", err)
	}
	if err := c.Snapshot(ctx, 200, "lum-a", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("a VM is not a container: err = %v, want ErrNotFound", err)
	}
}

func TestResources(t *testing.T) {
	f, srv, fp := newFake(t)
	c := client(srv.URL, fp)
	ctx := context.Background()

	// Neither cores (all the host's cores) nor memory (default 512 MiB) set.
	if r, err := c.Resources(ctx, 101); err != nil || r != (Resources{Cores: 0, Memory: 512}) {
		t.Fatalf("defaults: %+v, %v", r, err)
	}
	f.config["cores"], f.config["memory"] = 1, 1024
	if r, err := c.Resources(ctx, 101); err != nil || r != (Resources{Cores: 1, Memory: 1024}) {
		t.Fatalf("resources: %+v, %v", r, err)
	}

	if err := c.SetResources(ctx, 101, Resources{Memory: 4096}); err != nil {
		t.Fatal(err)
	}
	if f.config["memory"] != "4096" || f.config["cores"] != 1 {
		t.Errorf("only the memory must change: %v", f.config)
	}
	if err := c.SetResources(ctx, 101, Resources{Cores: 4, Memory: 2048}); err != nil {
		t.Fatal(err)
	}
	if f.config["cores"] != "4" || f.config["memory"] != "2048" {
		t.Errorf("config = %v", f.config)
	}

	f.calls = nil
	if err := c.SetResources(ctx, 101, Resources{}); err != nil || slices.Contains(f.calls, "PUT /nodes/pve1/lxc/101/config") {
		t.Errorf("nothing to change: no request (%v, %v)", f.calls, err)
	}
	if _, err := c.Resources(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown container: %v", err)
	}
}
