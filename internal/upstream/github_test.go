package upstream

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wolfx56/lum/internal/config"
)

// fakeGitHub serves canned responses per path and records the requests.
type fakeGitHub struct {
	t        *testing.T
	mu       sync.Mutex
	routes   map[string]string // path?query → JSON body (missing → 404)
	etag     string
	requests []*http.Request
	status   int // forced status when non-zero
	header   http.Header
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r.Clone(context.Background()))
	f.mu.Unlock()

	w.Header().Set("X-RateLimit-Limit", "60")
	w.Header().Set("X-RateLimit-Remaining", "42")
	w.Header().Set("X-RateLimit-Reset", "1792310400")
	for k, v := range f.header {
		w.Header()[k] = v
	}
	if f.status != 0 {
		w.WriteHeader(f.status)
		return
	}
	body, ok := f.routes[r.URL.RequestURI()]
	if !ok {
		http.NotFound(w, r)
		return
	}
	if f.etag != "" {
		if r.Header.Get("If-None-Match") == f.etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", f.etag)
	}
	_, _ = w.Write([]byte(body))
}

func (f *fakeGitHub) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func newClient(t *testing.T, f *fakeGitHub, opts GitHubOptions) *GitHub {
	t.Helper()
	f.t = t
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	opts.BaseURL = srv.URL
	return NewGitHub(opts)
}

func gh(repo string) config.Upstream {
	return config.Upstream{Type: config.UpstreamGitHub, Repo: repo, TagRegex: config.DefaultTagRegex}
}

const sonarrLatest = `{"tag_name":"v4.0.10.2544","name":"4.0.10.2544","html_url":"https://github.com/Sonarr/Sonarr/releases/tag/v4.0.10.2544","body":"## Changes\n- fix","published_at":"2026-10-01T10:00:00Z"}`

func TestLatestRelease(t *testing.T) {
	f := &fakeGitHub{routes: map[string]string{"/repos/Sonarr/Sonarr/releases/latest": sonarrLatest}}
	c := newClient(t, f, GitHubOptions{Token: "test-token"})

	rel, err := c.Latest(context.Background(), gh("Sonarr/Sonarr"))
	if err != nil {
		t.Fatal(err)
	}
	if rel.Version != "4.0.10.2544" || rel.Tag != "v4.0.10.2544" || !strings.Contains(rel.Body, "fix") || rel.PublishedAt.IsZero() {
		t.Errorf("unexpected release: %+v", rel)
	}

	req := f.requests[0]
	if req.Header.Get("Authorization") != "Bearer test-token" || req.Header.Get("Accept") != "application/vnd.github+json" {
		t.Errorf("missing headers: %v", req.Header)
	}
	if rl := c.RateLimit(); !rl.Known || rl.Limit != 60 || rl.Remaining != 42 || rl.Reset.Unix() != 1792310400 {
		t.Errorf("rate limit not recorded: %+v", rl)
	}
}

func TestCacheTTLAndETag(t *testing.T) {
	now := time.Date(2026, 10, 18, 7, 0, 0, 0, time.UTC)
	f := &fakeGitHub{etag: `"abc"`, routes: map[string]string{"/repos/Sonarr/Sonarr/releases/latest": sonarrLatest}}
	c := newClient(t, f, GitHubOptions{CacheTTL: 15 * time.Minute, Now: func() time.Time { return now }})
	ctx := context.Background()

	if _, err := c.Latest(ctx, gh("Sonarr/Sonarr")); err != nil {
		t.Fatal(err)
	}
	// Within the TTL: no request at all.
	now = now.Add(10 * time.Minute)
	if _, err := c.Latest(ctx, gh("Sonarr/Sonarr")); err != nil {
		t.Fatal(err)
	}
	if f.count() != 1 {
		t.Fatalf("requests within TTL = %d, want 1", f.count())
	}
	// After the TTL: conditional request, 304, cached body reused.
	now = now.Add(10 * time.Minute)
	rel, err := c.Latest(ctx, gh("Sonarr/Sonarr"))
	if err != nil {
		t.Fatal(err)
	}
	if f.count() != 2 || f.requests[1].Header.Get("If-None-Match") != `"abc"` {
		t.Fatalf("expected a conditional request, got %d requests", f.count())
	}
	if rel.Version != "4.0.10.2544" {
		t.Errorf("304 must reuse the cached release, got %+v", rel)
	}
	// The 304 refreshed the entry: within the TTL again.
	if _, err := c.Latest(ctx, gh("Sonarr/Sonarr")); err != nil || f.count() != 2 {
		t.Fatalf("304 must refresh the cache (requests=%d, err=%v)", f.count(), err)
	}
}

func TestIncludePrerelease(t *testing.T) {
	f := &fakeGitHub{routes: map[string]string{
		"/repos/a/b/releases?per_page=30": `[
			{"tag_name":"v2.0.0-beta.2","draft":true},
			{"tag_name":"nightly-2026-10-17","prerelease":true},
			{"tag_name":"v2.0.0-beta.1","prerelease":true,"html_url":"https://github.com/a/b/releases/tag/v2.0.0-beta.1"},
			{"tag_name":"v1.9.0"}
		]`,
	}}
	c := newClient(t, f, GitHubOptions{})
	u := gh("a/b")
	u.IncludePrerelease = true
	u.TagRegex = `^v(?P<version>\d+\.\d+\.\d+(-[0-9A-Za-z.]+)?)$`

	rel, err := c.Latest(context.Background(), u)
	if err != nil {
		t.Fatal(err)
	}
	if rel.Version != "2.0.0-beta.1" || !rel.Prerelease {
		t.Errorf("want the most recent non-draft matching release, got %+v", rel)
	}
}

func TestFallbackToTags(t *testing.T) {
	f := &fakeGitHub{routes: map[string]string{
		"/repos/a/b/tags?per_page=100": `[{"name":"v1.9.0"},{"name":"v1.10.0"},{"name":"v2.0.0-rc.1"},{"name":"latest"},{"name":"v1.2.0"}]`,
	}}
	c := newClient(t, f, GitHubOptions{})
	u := gh("a/b")
	u.TagRegex = `^v(?P<version>\d+\.\d+\.\d+(-[0-9A-Za-z.]+)?)$`

	rel, err := c.Latest(context.Background(), u)
	if err != nil {
		t.Fatal(err)
	}
	// Highest version, numeric comparison, prereleases excluded.
	if rel.Version != "1.10.0" || rel.Tag != "v1.10.0" || rel.URL != "https://github.com/a/b/releases/tag/v1.10.0" {
		t.Errorf("unexpected tag release: %+v", rel)
	}
	if f.count() != 2 {
		t.Errorf("want /releases/latest then /tags, got %d requests", f.count())
	}
}

func TestErrors(t *testing.T) {
	cases := []struct {
		name   string
		fake   *fakeGitHub
		want   error
		substr string
	}{
		{"repo not found", &fakeGitHub{routes: map[string]string{}}, ErrNotFound, "a/b"},
		{"tag does not match", &fakeGitHub{routes: map[string]string{"/repos/a/b/releases/latest": `{"tag_name":"release-candidate"}`}}, ErrNoMatchingVersion, "release-candidate"},
		{"no matching tag", &fakeGitHub{routes: map[string]string{"/repos/a/b/tags?per_page=100": `[{"name":"latest"}]`}}, ErrNoMatchingVersion, "1 tags"},
		{"unauthorized", &fakeGitHub{status: http.StatusUnauthorized}, nil, "check github.token"},
		{"rate limited", &fakeGitHub{status: http.StatusForbidden, header: http.Header{"X-Ratelimit-Remaining": {"0"}}}, nil, "rate limit exceeded"},
		{"server error", &fakeGitHub{status: http.StatusBadGateway}, nil, "unexpected status 502"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newClient(t, tc.fake, GitHubOptions{})
			_, err := c.Latest(context.Background(), gh("a/b"))
			if err == nil {
				t.Fatal("expected an error")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Errorf("error %v is not %v", err, tc.want)
			}
			if !strings.Contains(err.Error(), tc.substr) {
				t.Errorf("error %q does not contain %q", err, tc.substr)
			}
		})
	}
}

func TestTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()
	c := NewGitHub(GitHubOptions{BaseURL: srv.URL, Client: &http.Client{Timeout: 50 * time.Millisecond}})
	if _, err := c.Latest(context.Background(), gh("a/b")); err == nil {
		t.Fatal("expected a timeout error")
	}
}
