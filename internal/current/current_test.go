package current

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wolfx56/lum/internal/agentproto"
	"github.com/Wolfx56/lum/internal/config"
)

func apiApp(url, jsonPath string) config.App {
	return config.App{ID: "app", Current: config.Current{
		Type: config.TypeAPI, URL: url, JSONPath: jsonPath, AuthHeader: "X-Api-Key", Token: "test-key",
	}}
}

func serve(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "test-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCurrentFromAPI(t *testing.T) {
	cases := map[string]struct {
		body, path, want string
	}{
		"sonarr":       {`{"appName":"Sonarr","version":"4.0.9.2244"}`, "$.version", "4.0.9.2244"},
		"nested":       {`{"response":{"data":{"tautulli_version":"v2.15.3"}}}`, "$.response.data.tautulli_version", "v2.15.3"},
		"array":        {`{"items":[{"v":" 1.2.3 "}]}`, "$.items[0].v", "1.2.3"},
		"number value": {`{"build":42}`, "$.build", "42"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := serve(t, http.StatusOK, tc.body)
			got, err := NewReader(nil, nil).Current(context.Background(), apiApp(srv.URL, tc.path))
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCurrentErrors(t *testing.T) {
	cases := map[string]struct {
		status       int
		body, path   string
		token        config.Secret
		wantContains string
	}{
		"unauthorized":   {http.StatusOK, `{}`, "$.version", "wrong", "status 401, check the token"},
		"server error":   {http.StatusInternalServerError, ``, "$.version", "test-key", "status 500"},
		"invalid json":   {http.StatusOK, `<html>`, "$.version", "test-key", "not valid JSON"},
		"path not found": {http.StatusOK, `{"v":"1"}`, "$.version", "test-key", "matched nothing"},
		"not a scalar":   {http.StatusOK, `{"version":{"major":1}}`, "$.version", "test-key", "not a version string"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := serve(t, tc.status, tc.body)
			app := apiApp(srv.URL, tc.path)
			app.Current.Token = tc.token
			_, err := NewReader(nil, nil).Current(context.Background(), app)
			if err == nil || !strings.Contains(err.Error(), tc.wantContains) {
				t.Fatalf("got %v, want an error containing %q", err, tc.wantContains)
			}
		})
	}
}

func TestCurrentErrorsNeverLeakQuerySecrets(t *testing.T) {
	srv := serve(t, http.StatusBadGateway, "")
	app := apiApp(srv.URL+"/api/v2?cmd=info&apikey=test-query-secret", "$.version")
	_, err := NewReader(nil, nil).Current(context.Background(), app)
	if err == nil || strings.Contains(err.Error(), "test-query-secret") {
		t.Fatalf("error leaks the query secret: %v", err)
	}

	// Connection errors from http.Client embed the URL too.
	app = apiApp("http://127.0.0.1:1/api?apikey=test-query-secret", "$.version")
	_, err = NewReader(nil, nil).Current(context.Background(), app)
	if err == nil || strings.Contains(err.Error(), "test-query-secret") {
		t.Fatalf("connection error leaks the query secret: %v", err)
	}
}

func TestCurrentTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()
	r := NewReader(&http.Client{Timeout: 50 * time.Millisecond}, nil)
	if _, err := r.Current(context.Background(), apiApp(srv.URL, "$.version")); err == nil {
		t.Fatal("expected a timeout error")
	}
}

func TestCurrentAgentUnavailable(t *testing.T) {
	app := config.App{Current: config.Current{Type: config.TypeAgent, Command: []string{"x"}}}
	if _, err := NewReader(nil, nil).Current(context.Background(), app); !errors.Is(err, ErrAgentUnavailable) {
		t.Fatalf("want ErrAgentUnavailable, got %v", err)
	}
}

type fakeAgents struct {
	res agentproto.GetVersionResult
	err error
	got []string
}

func (f *fakeAgents) RunCommand(_ context.Context, agentID string, command []string) (agentproto.GetVersionResult, error) {
	f.got = append([]string{agentID}, command...)
	return f.res, f.err
}

func TestCurrentFromAgent(t *testing.T) {
	app := config.App{Agent: "vw-lxc", Current: config.Current{
		Type: config.TypeAgent, Command: []string{"/opt/vaultwarden/bin/vaultwarden", "--version"},
		OutputRegex: `(?P<version>\d+\.\d+\.\d+)`,
	}}
	cases := map[string]struct {
		res     agentproto.GetVersionResult
		err     error
		want    string
		wantErr string
	}{
		"stdout":          {res: agentproto.GetVersionResult{Stdout: "Vaultwarden 1.34.3\n"}, want: "1.34.3"},
		"stderr fallback": {res: agentproto.GetVersionResult{Stderr: "version 1.2.3"}, want: "1.2.3"},
		"exit code":       {res: agentproto.GetVersionResult{ExitCode: 127, Stderr: "not found\nmore"}, wantErr: `exit code 127 ("not found")`},
		"no match":        {res: agentproto.GetVersionResult{Stdout: "hello"}, wantErr: "output_regex matched nothing"},
		"not connected":   {err: fmt.Errorf("%w (vw-lxc)", ErrAgentUnavailable), wantErr: "no connected agent"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			agents := &fakeAgents{res: tc.res, err: tc.err}
			got, err := NewReader(nil, agents).Current(context.Background(), app)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
			if strings.Join(agents.got, " ") != "vw-lxc /opt/vaultwarden/bin/vaultwarden --version" {
				t.Errorf("command sent = %v", agents.got)
			}
		})
	}
}
