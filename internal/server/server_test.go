package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/gin-gonic/gin"

	"github.com/Wolfx56/lum/internal/httperr"
)

func init() { gin.SetMode(gin.TestMode) }

func newTestServer(t *testing.T, ui fstest.MapFS) *httptest.Server {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	var h http.Handler
	if ui == nil {
		h = New(Options{Logger: logger})
	} else {
		h = New(Options{Logger: logger, UI: ui})
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestHealthz(t *testing.T) {
	srv := newTestServer(t, nil)
	status, body := get(t, srv.URL+"/healthz")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if got["status"] != "ok" || got["version"] == "" {
		t.Errorf("unexpected body: %s", body)
	}
}

func TestUnknownRouteReturnsUniformError(t *testing.T) {
	srv := newTestServer(t, nil)
	for _, p := range []string{"/api/v1/nope", "/dashboard"} {
		status, body := get(t, srv.URL+p)
		if status != http.StatusNotFound {
			t.Fatalf("%s: status = %d, want 404", p, status)
		}
		var got httperr.Body
		if err := json.Unmarshal([]byte(body), &got); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if got.Error.Code != "not_found" {
			t.Errorf("%s: code = %q, want not_found", p, got.Error.Code)
		}
	}
}

func TestEmbeddedUI(t *testing.T) {
	srv := newTestServer(t, fstest.MapFS{
		"index.html":           {Data: []byte("<app-root></app-root>")},
		"main.js":              {Data: []byte("console.log('lum')")},
		"manifest.webmanifest": {Data: []byte(`{"name": "LUM"}`)},
	})

	cases := map[string]string{
		"/":        "<app-root>",
		"/main.js": "console.log",
		"/system":  "<app-root>", // SPA fallback
	}
	for p, want := range cases {
		status, body := get(t, srv.URL+p)
		if status != http.StatusOK || !strings.Contains(body, want) {
			t.Errorf("%s: status=%d body=%q, want 200 containing %q", p, status, body, want)
		}
	}

	res, err := http.Get(srv.URL + "/manifest.webmanifest")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if ct := res.Header.Get("Content-Type"); ct != "application/manifest+json" {
		t.Errorf("manifest Content-Type = %q, want application/manifest+json", ct)
	}

	if status, _ := get(t, srv.URL+"/api/v1/nope"); status != http.StatusNotFound {
		t.Errorf("/api/v1/nope: status = %d, want 404 even with UI", status)
	}
}
