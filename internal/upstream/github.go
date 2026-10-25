package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wolfx56/lum/internal/config"
	"github.com/Wolfx56/lum/internal/semver"
	"github.com/Wolfx56/lum/internal/version"
)

// DefaultGitHubURL is the public GitHub REST API.
const DefaultGitHubURL = "https://api.github.com"

const maxBody = 5 << 20 // release lists with long notes stay well below this

// GitHubOptions configures the GitHub client.
type GitHubOptions struct {
	Token    config.Secret // optional: 5000 req/h instead of 60
	CacheTTL time.Duration // responses younger than this are served without any request
	BaseURL  string        // defaults to DefaultGitHubURL (tests use httptest)
	Client   *http.Client  // defaults to a client with a 15s timeout
	Now      func() time.Time
}

// RateLimit is the GitHub quota reported by the last response.
type RateLimit struct {
	Known     bool      `json:"known"`
	Limit     int       `json:"limit"`
	Remaining int       `json:"remaining"`
	Reset     time.Time `json:"reset"`
}

// GitHub resolves releases through the GitHub REST API, with an ETag cache:
// within CacheTTL a cached response is reused as is; afterwards the request is
// conditional (If-None-Match), and a 304 does not consume quota.
type GitHub struct {
	opts GitHubOptions

	mu    sync.Mutex
	cache map[string]*cacheEntry
	rate  RateLimit
}

type cacheEntry struct {
	status  int // 200 or 404
	etag    string
	body    []byte
	fetched time.Time
}

// NewGitHub returns a GitHub client.
func NewGitHub(opts GitHubOptions) *GitHub {
	if opts.BaseURL == "" {
		opts.BaseURL = DefaultGitHubURL
	}
	opts.BaseURL = strings.TrimRight(opts.BaseURL, "/")
	if opts.Client == nil {
		opts.Client = &http.Client{Timeout: 15 * time.Second}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &GitHub{opts: opts, cache: map[string]*cacheEntry{}}
}

// RateLimit returns the quota reported by the last GitHub response.
func (g *GitHub) RateLimit() RateLimit {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.rate
}

type ghRelease struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	HTMLURL     string    `json:"html_url"`
	Body        string    `json:"body"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
}

type ghTag struct {
	Name string `json:"name"`
}

// Latest returns the latest release of u.Repo:
//   - include_prerelease false: /releases/latest (GitHub's latest stable release);
//   - include_prerelease true: the most recent non-draft release of /releases whose tag matches;
//   - no release at all: the highest matching version among /tags.
func (g *GitHub) Latest(ctx context.Context, u config.Upstream) (Release, error) {
	if u.Type != config.UpstreamGitHub {
		return Release{}, fmt.Errorf("unsupported upstream type %q", u.Type)
	}
	re, err := regexp.Compile(u.TagRegex)
	if err != nil {
		return Release{}, fmt.Errorf("tag_regex: %w", err)
	}
	repo := "/repos/" + u.Repo

	if !u.IncludePrerelease {
		var rel ghRelease
		found, err := g.getJSON(ctx, repo+"/releases/latest", &rel)
		if err != nil {
			return Release{}, err
		}
		if found {
			v, ok := normalize(re, rel.TagName)
			if !ok {
				return Release{}, fmt.Errorf("%w: latest release tag %q", ErrNoMatchingVersion, rel.TagName)
			}
			return fromRelease(rel, v), nil
		}
	} else {
		var rels []ghRelease
		found, err := g.getJSON(ctx, repo+"/releases?per_page=30", &rels)
		if err != nil {
			return Release{}, err
		}
		if !found {
			return Release{}, fmt.Errorf("%w: %s", ErrNotFound, u.Repo)
		}
		for _, rel := range rels {
			if rel.Draft {
				continue
			}
			if v, ok := normalize(re, rel.TagName); ok {
				return fromRelease(rel, v), nil
			}
		}
		if len(rels) > 0 {
			return Release{}, fmt.Errorf("%w: none of the %d latest releases", ErrNoMatchingVersion, len(rels))
		}
	}
	return g.latestTag(ctx, u, re)
}

// latestTag is the fallback for repositories that publish tags but no releases.
func (g *GitHub) latestTag(ctx context.Context, u config.Upstream, re *regexp.Regexp) (Release, error) {
	var tags []ghTag
	found, err := g.getJSON(ctx, "/repos/"+u.Repo+"/tags?per_page=100", &tags)
	if err != nil {
		return Release{}, err
	}
	if !found {
		return Release{}, fmt.Errorf("%w: %s", ErrNotFound, u.Repo)
	}
	var best *semver.Version
	var bestTag string
	for _, t := range tags {
		raw, ok := normalize(re, t.Name)
		if !ok {
			continue
		}
		v, err := semver.Parse(raw)
		if err != nil || (v.IsPrerelease() && !u.IncludePrerelease) {
			continue
		}
		if best == nil || semver.Compare(v, *best) > 0 {
			best, bestTag = &v, t.Name
		}
	}
	if best == nil {
		return Release{}, fmt.Errorf("%w: no release and none of the %d tags", ErrNoMatchingVersion, len(tags))
	}
	return Release{
		Version:    best.String(),
		Tag:        bestTag,
		URL:        "https://github.com/" + u.Repo + "/releases/tag/" + url.PathEscape(bestTag),
		Prerelease: best.IsPrerelease(),
	}, nil
}

func fromRelease(r ghRelease, v string) Release {
	return Release{
		Version:     v,
		Tag:         r.TagName,
		Name:        r.Name,
		URL:         r.HTMLURL,
		Body:        r.Body,
		PublishedAt: r.PublishedAt,
		Prerelease:  r.Prerelease,
	}
}

// getJSON decodes the response of path into out. found is false on a 404.
func (g *GitHub) getJSON(ctx context.Context, path string, out any) (found bool, err error) {
	status, body, err := g.get(ctx, path)
	if err != nil {
		return false, err
	}
	if status == http.StatusNotFound {
		return false, nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return false, fmt.Errorf("github %s: invalid JSON: %w", path, err)
	}
	return true, nil
}

// get performs a cached, conditional GET. It returns 200 or 404, anything else is an error.
func (g *GitHub) get(ctx context.Context, path string) (int, []byte, error) {
	now := g.opts.Now()
	g.mu.Lock()
	entry := g.cache[path]
	g.mu.Unlock()
	if entry != nil && now.Sub(entry.fetched) < g.opts.CacheTTL {
		return entry.status, entry.body, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.opts.BaseURL+path, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "lum/"+version.Version)
	if g.opts.Token.IsSet() {
		req.Header.Set("Authorization", "Bearer "+g.opts.Token.Reveal())
	}
	if entry != nil && entry.etag != "" {
		req.Header.Set("If-None-Match", entry.etag)
	}

	resp, err := g.opts.Client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("github %s: %w", path, err)
	}
	defer resp.Body.Close()
	g.updateRateLimit(resp.Header)

	switch resp.StatusCode {
	case http.StatusNotModified:
		if entry == nil {
			return 0, nil, fmt.Errorf("github %s: 304 without a cached response", path)
		}
		g.store(path, &cacheEntry{status: entry.status, etag: entry.etag, body: entry.body, fetched: now})
		return entry.status, entry.body, nil
	case http.StatusOK, http.StatusNotFound:
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
		if err != nil {
			return 0, nil, fmt.Errorf("github %s: reading response: %w", path, err)
		}
		g.store(path, &cacheEntry{status: resp.StatusCode, etag: resp.Header.Get("ETag"), body: body, fetched: now})
		return resp.StatusCode, body, nil
	case http.StatusUnauthorized:
		return 0, nil, fmt.Errorf("github %s: authentication failed (401), check github.token", path)
	case http.StatusForbidden, http.StatusTooManyRequests:
		if resp.Header.Get("X-RateLimit-Remaining") == "0" {
			return 0, nil, fmt.Errorf("github %s: rate limit exceeded, resets at %s", path, g.RateLimit().Reset.Format(time.RFC3339))
		}
		return 0, nil, fmt.Errorf("github %s: access denied (%d)", path, resp.StatusCode)
	default:
		return 0, nil, fmt.Errorf("github %s: unexpected status %d", path, resp.StatusCode)
	}
}

func (g *GitHub) store(path string, e *cacheEntry) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.cache[path] = e
}

func (g *GitHub) updateRateLimit(h http.Header) {
	limit, err1 := strconv.Atoi(h.Get("X-RateLimit-Limit"))
	remaining, err2 := strconv.Atoi(h.Get("X-RateLimit-Remaining"))
	reset, err3 := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.rate = RateLimit{Known: true, Limit: limit, Remaining: remaining, Reset: time.Unix(reset, 0)}
}
