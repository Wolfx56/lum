package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Wolfx56/lum/internal/auth"
	"github.com/Wolfx56/lum/internal/config"
	"github.com/Wolfx56/lum/internal/service"
	"github.com/Wolfx56/lum/internal/store"
)

// Test placeholders: they must never appear in any API response.
var secrets = map[string]string{
	"SONARR_API_KEY":   "test-secret-sonarr-key",
	"TAUTULLI_API_KEY": "test-secret-tautulli-key",
	"RADARR_API_KEY":   "test-secret-radarr-key",
	"GITHUB_TOKEN":     "test-secret-github-token",
	"HOOK_URL":         "http://127.0.0.1:1/hook/test-secret-hook-id",
	"AGENT_SECRET":     "test-secret-agent-bootstrap",
}

const serverYAML = `
auth:
  mode: none
github:
  token: "${GITHUB_TOKEN}"
  cache_ttl: 0s
notifications:
  - type: webhook
    url: "${HOOK_URL}"
    on: [job_failed]
agents:
  shared_secret: "${AGENT_SECRET}"
scripts:
  dir: scripts
`

func appsYAML(appURL string) string {
	return `
apps:
  - id: sonarr
    name: Sonarr
    lxc_id: 101
    upstream: {type: github, repo: Sonarr/Sonarr}
    current:
      type: api
      url: "` + appURL + `/sonarr"
      auth_header: X-Api-Key
      token: "${SONARR_API_KEY}"
      json_path: "$.version"
    update:
      type: api
      url: "` + appURL + `/sonarr/update"
      auth_header: X-Api-Key
      token: "${SONARR_API_KEY}"
      timeout: 1m
  - id: tautulli
    name: Tautulli
    lxc_id: 120
    upstream: {type: github, repo: Tautulli/Tautulli}
    current:
      type: api
      url: "` + appURL + `/tautulli?cmd=info&apikey=${TAUTULLI_API_KEY}"
      json_path: "$.version"
  - id: radarr
    name: Radarr
    lxc_id: 106
    upstream: {type: github, repo: Radarr/Radarr}
    current:
      type: api
      url: "` + appURL + `/radarr"
      auth_header: X-Api-Key
      token: "${RADARR_API_KEY}"
      json_path: "$.version"
  - id: vaultwarden
    name: Vaultwarden
    lxc_id: 102
    agent: vw-lxc
    upstream: {type: github, repo: dani-garcia/vaultwarden}
    current:
      type: agent
      command: ["cat", "/nonexistent/version"]
      output_regex: '(?P<version>\d+\.\d+\.\d+)'
    update:
      type: script
      script: up.sh
`
}

type apiEnv struct {
	srv   *httptest.Server
	dir   string
	store *store.Store
	svc   *service.Service
}

func newAPIEnv(t *testing.T) *apiEnv {
	t.Helper()
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		repo := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/repos/"), "/releases/latest")
		tags := map[string]string{"Sonarr/Sonarr": "v4.0.10.2544", "Tautulli/Tautulli": "v2.15.3", "Radarr/Radarr": "v5.0.0.1"}
		tag, ok := tags[repo]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": tag, "html_url": "https://github.com/" + repo + "/releases/tag/" + tag, "body": "## " + tag + "\n- fixes"})
	}))
	t.Cleanup(github.Close)

	apps := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sonarr":
			if r.Header.Get("X-Api-Key") == secrets["SONARR_API_KEY"] {
				_, _ = w.Write([]byte(`{"version":"4.0.9.2244"}`))
				return
			}
		case "/sonarr/update":
			// Accepted, but the version never changes: the job runs until cancelled.
			if r.Header.Get("X-Api-Key") == secrets["SONARR_API_KEY"] {
				w.WriteHeader(http.StatusCreated)
				return
			}
		case "/tautulli":
			if r.URL.Query().Get("apikey") == secrets["TAUTULLI_API_KEY"] {
				_, _ = w.Write([]byte(`{"version":"v2.15.3"}`))
				return
			}
		}
		w.WriteHeader(http.StatusUnauthorized) // radarr: always refused
	}))
	t.Cleanup(apps.Close)

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "server.yaml"), serverYAML)
	writeFile(t, filepath.Join(dir, "apps.yaml"), appsYAML(apps.URL))
	if err := os.Mkdir(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scripts", "up.sh"), []byte("#!/usr/bin/env bash\necho up\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	holder, err := config.NewHolder(config.Options{Dir: dir, LookupEnv: func(k string) (string, bool) { v, ok := secrets[k]; return v, ok }})
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "lum.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	logger := slog.New(slog.DiscardHandler)
	svc, err := service.New(service.Options{Holder: holder, Store: st, Logger: logger, GitHubURL: github.URL})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Stop)

	h := New(Options{Logger: logger, Service: svc, Auth: auth.New(auth.Options{Config: holder.Current().Auth, Store: st})})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &apiEnv{srv: srv, dir: dir, store: st, svc: svc}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (e *apiEnv) do(t *testing.T, method, path string, header map[string]string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, e.srv.URL+path, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func decode[T any](t *testing.T, body string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("invalid JSON %q: %v", body, err)
	}
	return v
}

type appsResponse struct {
	Apps []struct {
		ID          string  `json:"id"`
		Status      string  `json:"status"`
		LastCheckAt *string `json:"last_check_at"`
	} `json:"apps"`
}

func TestAPIDashboardFlow(t *testing.T) {
	e := newAPIEnv(t)

	// Before any check: every configured app is listed as unknown.
	status, body := e.do(t, http.MethodGet, "/api/v1/apps", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /apps: %d %s", status, body)
	}
	before := decode[appsResponse](t, body)
	if len(before.Apps) != 4 || before.Apps[0].Status != "unknown" || before.Apps[0].LastCheckAt != nil {
		t.Fatalf("before check: %s", body)
	}

	if status, body := e.do(t, http.MethodPost, "/api/v1/checks", nil); status != http.StatusOK {
		t.Fatalf("POST /checks: %d %s", status, body)
	}

	_, body = e.do(t, http.MethodGet, "/api/v1/apps", nil)
	got := map[string]string{}
	for _, a := range decode[appsResponse](t, body).Apps {
		got[a.ID] = a.Status
	}
	want := map[string]string{"sonarr": "update_available", "tautulli": "up_to_date", "radarr": "error"}
	for id, s := range want {
		if got[id] != s {
			t.Errorf("%s: status %q, want %q (%s)", id, got[id], s, body)
		}
	}
	if !strings.Contains(body, "unexpected status 401") {
		t.Errorf("the radarr error must be visible: %s", body)
	}

	// Detail: release notes, history, recipe without secrets.
	status, body = e.do(t, http.MethodGet, "/api/v1/apps/tautulli", nil)
	if status != http.StatusOK || !strings.Contains(body, `"release_notes":"## v2.15.3`) || !strings.Contains(body, "apikey=****") {
		t.Errorf("GET /apps/tautulli: %d %s", status, body)
	}

	// Single check and unknown app.
	if status, body := e.do(t, http.MethodPost, "/api/v1/apps/sonarr/check", nil); status != http.StatusOK || !strings.Contains(body, `"status":"update_available"`) {
		t.Errorf("POST /apps/sonarr/check: %d %s", status, body)
	}
	if status, body := e.do(t, http.MethodPost, "/api/v1/apps/nope/check", nil); status != http.StatusNotFound || !strings.Contains(body, `"code":"app_not_found"`) {
		t.Errorf("unknown app: %d %s", status, body)
	}

	// System status.
	status, body = e.do(t, http.MethodGet, "/api/v1/system/status", nil)
	if status != http.StatusOK || !strings.Contains(body, `"valid":true`) || !strings.Contains(body, `"app_id":"radarr"`) || !strings.Contains(body, `"token_configured":true`) {
		t.Errorf("GET /system/status: %d %s", status, body)
	}

	// Metrics.
	status, body = e.do(t, http.MethodGet, "/metrics", nil)
	if status != http.StatusOK || !strings.Contains(body, `lum_apps{status="update_available"} 1`) || !strings.Contains(body, `lum_checks_total{status="error"}`) {
		t.Errorf("GET /metrics: %d (missing LUM metrics)", status)
	}

	// Audit trail of the write actions.
	entries, err := e.store.AuditEntries(context.Background(), 10)
	if err != nil || len(entries) != 2 || entries[0].Action != "app.check" || entries[1].Action != "checks.run" || entries[0].User != "anonymous" {
		t.Errorf("audit = %+v (%v)", entries, err)
	}
}

// Acceptance criterion §6.2: no secret in API responses.
func TestAPINeverReturnsSecrets(t *testing.T) {
	e := newAPIEnv(t)
	e.do(t, http.MethodPost, "/api/v1/checks", nil)

	var all strings.Builder
	for _, p := range []string{"/api/v1/apps", "/api/v1/apps/sonarr", "/api/v1/apps/tautulli", "/api/v1/apps/radarr", "/api/v1/system/status", "/api/v1/me", "/metrics"} {
		_, body := e.do(t, http.MethodGet, p, nil)
		all.WriteString(body)
	}
	_, body := e.do(t, http.MethodPost, "/api/v1/apps/radarr/check", nil)
	all.WriteString(body)

	for name, value := range secrets {
		if strings.Contains(all.String(), value) {
			t.Errorf("%s leaked in an API response", name)
		}
	}
}

func TestAPIConfigReload(t *testing.T) {
	e := newAPIEnv(t)
	good, _ := os.ReadFile(filepath.Join(e.dir, "apps.yaml"))

	writeFile(t, filepath.Join(e.dir, "apps.yaml"), "apps:\n  - id: Bad ID\n    lxc_id: 0\n")
	status, body := e.do(t, http.MethodPost, "/api/v1/config/reload", nil)
	if status != http.StatusUnprocessableEntity || !strings.Contains(body, `"code":"invalid_config"`) || !strings.Contains(body, `"path":"apps[0].lxc_id"`) {
		t.Fatalf("invalid reload: %d %s", status, body)
	}
	_, body = e.do(t, http.MethodGet, "/api/v1/system/status", nil)
	if !strings.Contains(body, `"valid":false`) || !strings.Contains(body, `"kind":"validation"`) {
		t.Errorf("status after a failed reload: %s", body)
	}
	if _, apps := e.do(t, http.MethodGet, "/api/v1/apps", nil); !strings.Contains(apps, `"id":"sonarr"`) {
		t.Error("the previous configuration must stay active")
	}

	writeFile(t, filepath.Join(e.dir, "apps.yaml"), string(good))
	if status, body := e.do(t, http.MethodPost, "/api/v1/config/reload", nil); status != http.StatusOK || !strings.Contains(body, `"apps":4`) {
		t.Fatalf("valid reload: %d %s", status, body)
	}
	if _, body := e.do(t, http.MethodGet, "/api/v1/system/status", nil); !strings.Contains(body, `"valid":true`) {
		t.Errorf("status after a good reload: %s", body)
	}
}

func TestAPIRejectsCrossOriginWrites(t *testing.T) {
	e := newAPIEnv(t)
	status, body := e.do(t, http.MethodPost, "/api/v1/checks", map[string]string{"Origin": "https://evil.example.com"})
	if status != http.StatusForbidden || !strings.Contains(body, `"code":"csrf"`) {
		t.Errorf("cross-origin POST: %d %s", status, body)
	}
}

func TestAgentEndpointBypassesUserAuth(t *testing.T) {
	e := newAPIEnv(t)
	oidc := auth.New(auth.Options{
		Config:  config.Auth{Mode: config.AuthOIDC, OIDC: config.OIDC{Issuer: "https://idp.invalid/realms/x", ClientID: "lum"}},
		BaseURL: "https://lum.example.com",
		Store:   e.store,
	})
	srv := httptest.NewServer(New(Options{Logger: slog.New(slog.DiscardHandler), Service: e.svc, Auth: oidc, BaseURL: "https://lum.example.com"}))
	defer srv.Close()

	get := func(path string) int {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if s := get("/api/v1/apps"); s != http.StatusUnauthorized {
		t.Errorf("/api/v1/apps without session: %d, want 401", s)
	}
	// The agent endpoint is reachable without a user session: a plain GET is
	// answered by the WebSocket handler (426 Upgrade Required), not by the auth.
	if s := get("/agent/ws"); s != http.StatusUpgradeRequired {
		t.Errorf("/agent/ws: %d, want 426", s)
	}
}
