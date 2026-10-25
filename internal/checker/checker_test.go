package checker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wolfx56/lum/internal/config"
	"github.com/Wolfx56/lum/internal/current"
	"github.com/Wolfx56/lum/internal/store"
	"github.com/Wolfx56/lum/internal/upstream"
)

// fakes returning versions from mutable maps.
type fakeUpstream struct {
	mu     sync.Mutex
	latest map[string]string // repo → version
	err    map[string]error
}

func (f *fakeUpstream) Latest(_ context.Context, u config.Upstream) (upstream.Release, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.err[u.Repo]; err != nil {
		return upstream.Release{}, err
	}
	v := f.latest[u.Repo]
	return upstream.Release{Version: v, Tag: "v" + v, URL: "https://github.com/" + u.Repo + "/releases/tag/v" + v, Body: "notes " + v}, nil
}

func (f *fakeUpstream) set(repo, v string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.latest[repo] = v
}

type fakeCurrent struct {
	installed map[string]string // app id → version
	err       map[string]error
	delay     time.Duration
	inFlight  atomic.Int32
	maxFlight atomic.Int32
}

func (f *fakeCurrent) Current(ctx context.Context, app config.App) (string, error) {
	n := f.inFlight.Add(1)
	defer f.inFlight.Add(-1)
	for {
		m := f.maxFlight.Load()
		if n <= m || f.maxFlight.CompareAndSwap(m, n) {
			break
		}
	}
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if err := f.err[app.ID]; err != nil {
		return "", err
	}
	return f.installed[app.ID], nil
}

// hookRecorder counts webhook notifications.
type hookRecorder struct {
	mu     sync.Mutex
	bodies []string
}

func (h *hookRecorder) ServeHTTP(_ http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	h.mu.Lock()
	h.bodies = append(h.bodies, string(b))
	h.mu.Unlock()
}

func (h *hookRecorder) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.bodies)
}

func app(id string) config.App {
	return config.App{ID: id, Name: strings.ToUpper(id[:1]) + id[1:], LXCID: 1,
		Upstream: config.Upstream{Type: config.UpstreamGitHub, Repo: "o/" + id},
		Current:  config.Current{Type: config.TypeAPI}}
}

type env struct {
	checker *Checker
	store   *store.Store
	up      *fakeUpstream
	cur     *fakeCurrent
	hook    *hookRecorder
}

func newEnv(t *testing.T, apps ...config.App) *env {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "lum.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	hook := &hookRecorder{}
	srv := httptest.NewServer(hook)
	t.Cleanup(srv.Close)

	cfg := &config.Config{
		Apps:      apps,
		Scheduler: config.Scheduler{Concurrency: 5},
		Notifications: []config.Notification{{
			Type: config.NotifyWebhook, URL: config.Secret(srv.URL + "/hook"), On: []string{config.EventUpdateAvailable},
		}},
	}
	e := &env{
		store: st,
		up:    &fakeUpstream{latest: map[string]string{}, err: map[string]error{}},
		cur:   &fakeCurrent{installed: map[string]string{}, err: map[string]error{}},
		hook:  hook,
	}
	e.checker = New(Options{Config: func() *config.Config { return cfg }, Upstream: e.up, Current: e.cur, Store: st})
	return e
}

func TestNotifiesOncePerNewVersion(t *testing.T) {
	e := newEnv(t, app("sonarr"))
	ctx := context.Background()
	e.cur.installed["sonarr"] = "4.0.9"

	steps := []struct {
		latest     string
		wantStatus string
		wantHooks  int
	}{
		{"4.0.9", "up_to_date", 0},
		{"4.0.10", "update_available", 1}, // transition → notified
		{"4.0.10", "update_available", 1}, // same version → not re-notified
		{"4.0.11", "update_available", 2}, // newer version → notified
	}
	for i, s := range steps {
		e.up.set("o/sonarr", s.latest)
		res, err := e.checker.CheckApp(ctx, "sonarr")
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != s.wantStatus || e.hook.count() != s.wantHooks {
			t.Fatalf("step %d: status=%s hooks=%d, want %s/%d", i, res.Status, e.hook.count(), s.wantStatus, s.wantHooks)
		}
	}
	if !strings.Contains(e.hook.bodies[0], `"latest_version":"4.0.10"`) || !strings.Contains(e.hook.bodies[0], "Sonarr: update available") {
		t.Errorf("unexpected payload: %s", e.hook.bodies[0])
	}

	st, err := e.store.AppState(ctx, "sonarr")
	if err != nil || st.LastNotifiedVersion != "4.0.11" {
		t.Errorf("state = %+v (%v)", st, err)
	}
	checks, _ := e.store.Checks(ctx, "sonarr", 10)
	if len(checks) != 4 || checks[0].ReleaseNotes != "notes 4.0.11" {
		t.Errorf("history = %d checks, latest notes %q", len(checks), checks[0].ReleaseNotes)
	}
}

func TestFailuresAreIsolated(t *testing.T) {
	e := newEnv(t, app("sonarr"), app("radarr"), app("prowlarr"), app("vaultwarden"))
	e.up.latest = map[string]string{"o/sonarr": "4.0.0", "o/radarr": "5.0.0", "o/vaultwarden": "1.32.0"}
	e.up.err["o/prowlarr"] = errors.New("github: rate limit exceeded")
	e.cur.installed = map[string]string{"sonarr": "4.0.0", "prowlarr": "1.0.0"}
	e.cur.err["radarr"] = errors.New("GET http://192.0.2.16/api: unexpected status 401, check the token")
	e.cur.err["vaultwarden"] = current.ErrAgentUnavailable

	results := e.checker.CheckAll(context.Background())

	want := map[string]string{"sonarr": "up_to_date", "radarr": "error", "prowlarr": "error", "vaultwarden": "unknown"}
	states, err := e.store.AppStates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range results {
		if r.Status != want[r.AppID] || states[r.AppID].Status != want[r.AppID] {
			t.Errorf("results[%d] %s: status=%s stored=%s, want %s", i, r.AppID, r.Status, states[r.AppID].Status, want[r.AppID])
		}
	}
	if s := states["radarr"]; !strings.Contains(s.Error, "installed version: GET") || s.LatestVersion != "5.0.0" {
		t.Errorf("radarr: error must be stored and the latest version kept: %+v", s)
	}
	if s := states["prowlarr"]; !strings.Contains(s.Error, "latest version: github: rate limit") {
		t.Errorf("prowlarr error = %q", s.Error)
	}
	if s := states["vaultwarden"]; !strings.Contains(s.Error, "no connected agent") {
		t.Errorf("vaultwarden must keep the reason of the unknown state: %q", s.Error)
	}
}

// Acceptance criterion §6.2: 20 apps checked in parallel within the concurrency limit.
func TestCheckAllConcurrency(t *testing.T) {
	var apps []config.App
	for i := range 20 {
		apps = append(apps, app(fmt.Sprintf("app%02d", i)))
	}
	e := newEnv(t, apps...)
	e.cur.delay = 100 * time.Millisecond
	for _, a := range apps {
		e.up.latest[a.Upstream.Repo] = "1.0.0"
		e.cur.installed[a.ID] = "1.0.0"
	}

	start := time.Now()
	results := e.checker.CheckAll(context.Background())
	elapsed := time.Since(start)

	if len(results) != 20 {
		t.Fatalf("got %d results", len(results))
	}
	if peak := e.cur.maxFlight.Load(); peak > 5 || peak < 2 {
		t.Errorf("max concurrent checks = %d, want parallelism capped at 5", peak)
	}
	// Sequential would take 2s; 5 at a time takes ~400ms.
	if elapsed > 1500*time.Millisecond {
		t.Errorf("CheckAll took %v, checks are not parallel", elapsed)
	}
}

func TestStoredErrorsNeverContainSecrets(t *testing.T) {
	appSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer appSrv.Close()

	a := app("tautulli")
	a.Current = config.Current{Type: config.TypeAPI, URL: appSrv.URL + "/api/v2?cmd=info&apikey=test-query-secret",
		JSONPath: "$.version", AuthHeader: "X-Api-Key", Token: "test-header-secret"}
	e := newEnv(t, a)
	e.up.latest["o/tautulli"] = "2.15.0"
	e.checker.opts.Current = current.NewReader(nil, nil)

	res, err := e.checker.CheckApp(context.Background(), "tautulli")
	if err != nil {
		t.Fatal(err)
	}
	st, _ := e.store.AppState(context.Background(), "tautulli")
	for _, s := range []string{res.Error, st.Error} {
		if s == "" || strings.Contains(s, "test-query-secret") || strings.Contains(s, "test-header-secret") {
			t.Errorf("stored error leaks a secret or is empty: %q", s)
		}
	}
}

func TestCheckUnknownApp(t *testing.T) {
	e := newEnv(t)
	if _, err := e.checker.CheckApp(context.Background(), "absent"); !errors.Is(err, ErrUnknownApp) {
		t.Fatalf("want ErrUnknownApp, got %v", err)
	}
}
