package config

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/ohler55/ojg/jp"
	"github.com/robfig/cron/v3"

	"github.com/Wolfx56/lum/internal/logging"
)

// Problem is one validation failure, located by its config path.
type Problem struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// ValidationError lists every problem found in the configuration.
type ValidationError struct {
	Problems []Problem
}

func (e *ValidationError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "invalid configuration (%d problems):", len(e.Problems))
	for _, p := range e.Problems {
		fmt.Fprintf(&b, "\n  - %s: %s", p.Path, p.Message)
	}
	return b.String()
}

var kebabCase = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
var githubRepo = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

var (
	proxmoxTokenID  = regexp.MustCompile(`^[^@!\s]+@[^@!\s]+![A-Za-z0-9][A-Za-z0-9_.-]*$`)
	certFingerprint = regexp.MustCompile(`^[0-9A-Fa-f]{2}(:[0-9A-Fa-f]{2}){31}$`)
)

type validator struct {
	problems []Problem
}

func (v *validator) addf(path, format string, args ...any) {
	v.problems = append(v.problems, Problem{Path: path, Message: fmt.Sprintf(format, args...)})
}

func (v *validator) required(path, value string) bool {
	if value == "" {
		v.addf(path, "is required")
		return false
	}
	return true
}

func (v *validator) oneOf(path, value string, allowed ...string) {
	if !slices.Contains(allowed, value) {
		v.addf(path, "must be one of %s (got %q)", strings.Join(allowed, ", "), value)
	}
}

// httpURL checks an absolute http(s) URL. The value is never echoed: it may come from a secret.
func (v *validator) httpURL(path, value string) {
	if !v.required(path, value) {
		return
	}
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		v.addf(path, "must be an absolute http(s) URL")
	}
}

// versionRegex checks that a regex compiles and has a named group "version".
func (v *validator) versionRegex(path, value string) {
	if !v.required(path, value) {
		return
	}
	re, err := regexp.Compile(value)
	if err != nil {
		v.addf(path, "invalid regex: %v", err)
		return
	}
	if re.SubexpIndex("version") < 0 {
		v.addf(path, "must contain the named group (?P<version>...)")
	}
}

func (v *validator) forbidden(path string, set bool, reason string) {
	if set {
		v.addf(path, "is not allowed %s", reason)
	}
}

func validate(c *Config) error {
	v := &validator{}
	v.server(c)
	v.apps(c)
	if len(v.problems) > 0 {
		return &ValidationError{Problems: v.problems}
	}
	return nil
}

func (v *validator) server(c *Config) {
	v.required("server.listen", c.Server.Listen)
	if c.Server.BaseURL != "" {
		v.httpURL("server.base_url", c.Server.BaseURL)
	}
	if _, err := logging.ParseLevel(c.Server.LogLevel); err != nil {
		v.addf("server.log_level", "must be one of debug, info, warn, error (got %q)", c.Server.LogLevel)
	}

	v.oneOf("auth.mode", c.Auth.Mode, AuthOIDC, AuthNone)
	if c.Auth.Mode == AuthOIDC {
		if c.Server.BaseURL == "" {
			v.addf("server.base_url", "is required with auth.mode oidc (OIDC callback URL)")
		}
		v.httpURL("auth.oidc.issuer", c.Auth.OIDC.Issuer)
		v.required("auth.oidc.client_id", c.Auth.OIDC.ClientID)
		v.required("auth.oidc.client_secret", c.Auth.OIDC.ClientSecret.Reveal())
	}

	if c.Auth.SessionTTL <= 0 {
		v.addf("auth.session_ttl", "must be positive")
	}

	if c.GitHub.CacheTTL < 0 {
		v.addf("github.cache_ttl", "must not be negative")
	}

	if _, err := cron.ParseStandard(c.Scheduler.CheckCron); err != nil {
		v.addf("scheduler.check_cron", "invalid cron expression: %v", err)
	}
	if c.Scheduler.Jitter < 0 {
		v.addf("scheduler.jitter", "must not be negative")
	}
	if c.Scheduler.Concurrency < 1 {
		v.addf("scheduler.concurrency", "must be at least 1")
	}

	events := []string{EventUpdateAvailable, EventJobSuccess, EventJobFailed, EventAgentOffline}
	for i, n := range c.Notifications {
		p := fmt.Sprintf("notifications[%d]", i)
		v.oneOf(p+".type", n.Type, NotifyNtfy, NotifyWebhook, NotifyTelegram)
		v.httpURL(p+".url", n.URL.Reveal())
		if n.Type == NotifyTelegram {
			v.required(p+".chat_id", n.ChatID)
		} else {
			v.forbidden(p+".chat_id", n.ChatID != "", "for type "+n.Type)
		}
		if len(n.On) == 0 {
			v.addf(p+".on", "must list at least one event")
		}
		for j, e := range n.On {
			v.oneOf(fmt.Sprintf("%s.on[%d]", p, j), e, events...)
		}
	}

	if c.Proxmox.Configured() {
		v.httpURL("proxmox.url", c.Proxmox.URL)
		if v.required("proxmox.token_id", c.Proxmox.TokenID) && !proxmoxTokenID.MatchString(c.Proxmox.TokenID) {
			v.addf("proxmox.token_id", "must look like user@realm!name (got %q)", c.Proxmox.TokenID)
		}
		v.required("proxmox.token_secret", c.Proxmox.TokenSecret.Reveal())
		if c.Proxmox.Fingerprint != "" && !certFingerprint.MatchString(c.Proxmox.Fingerprint) {
			v.addf("proxmox.fingerprint", "must be a SHA-256 fingerprint: 32 hex bytes separated by colons")
		}
	} else {
		v.forbidden("proxmox.token_id", c.Proxmox.TokenID != "", "without proxmox.url")
		v.forbidden("proxmox.token_secret", c.Proxmox.TokenSecret.IsSet(), "without proxmox.url")
	}

	v.oneOf("agents.enrollment", c.Agents.Enrollment, EnrollmentOpen, EnrollmentClosed)
	v.oneOf("agents.self_update", c.Agents.SelfUpdate, SelfUpdateAuto, SelfUpdateOff)
	if c.Agents.HeartbeatInterval <= 0 {
		v.addf("agents.heartbeat_interval", "must be positive")
	}
	if c.Agents.OfflineAfter <= c.Agents.HeartbeatInterval {
		v.addf("agents.offline_after", "must be greater than agents.heartbeat_interval")
	}
}

func (v *validator) apps(c *Config) {
	seen := map[string]int{}
	for i := range c.Apps {
		a := &c.Apps[i]
		p := fmt.Sprintf("apps[%d]", i)

		if v.required(p+".id", a.ID) {
			if !kebabCase.MatchString(a.ID) {
				v.addf(p+".id", "must be kebab-case (got %q)", a.ID)
			}
			if j, dup := seen[a.ID]; dup {
				v.addf(p+".id", "duplicate id %q (already used by apps[%d])", a.ID, j)
			}
			seen[a.ID] = i
		}
		if a.LXCID <= 0 {
			v.addf(p+".lxc_id", "must be a positive integer")
		}
		if a.Agent != "" && !kebabCase.MatchString(a.Agent) {
			v.addf(p+".agent", "must be kebab-case (got %q)", a.Agent)
		}
		if a.Retention < 1 {
			v.addf(p+".retention", "must be at least 1")
		}

		v.upstream(p+".upstream", a.Upstream)
		v.current(p+".current", a)
		if a.Update != nil {
			v.update(p+".update", a, c.Scripts.Dir)
		}
		if h := a.Healthcheck; h != nil {
			v.httpURL(p+".healthcheck.url", h.URL)
			if h.ExpectStatus < 100 || h.ExpectStatus > 599 {
				v.addf(p+".healthcheck.expect_status", "must be an HTTP status code (got %d)", h.ExpectStatus)
			}
			if h.Timeout <= 0 {
				v.addf(p+".healthcheck.timeout", "must be positive")
			}
		}
	}
}

func (v *validator) upstream(p string, u Upstream) {
	v.oneOf(p+".type", u.Type, UpstreamGitHub)
	if v.required(p+".repo", u.Repo) && !githubRepo.MatchString(u.Repo) {
		v.addf(p+".repo", "must be owner/name (got %q)", u.Repo)
	}
	v.versionRegex(p+".tag_regex", u.TagRegex)
}

func (v *validator) current(p string, a *App) {
	cur := a.Current
	switch cur.Type {
	case TypeAPI:
		v.httpURL(p+".url", cur.URL)
		if v.required(p+".json_path", cur.JSONPath) {
			if _, err := jp.ParseString(cur.JSONPath); err != nil {
				v.addf(p+".json_path", "invalid JSONPath: %v", err)
			}
		}
		v.authPair(p, cur.AuthHeader, cur.Token)
		v.forbidden(p+".command", len(cur.Command) > 0, "for type api")
		v.forbidden(p+".output_regex", cur.OutputRegex != "", "for type api")
	case TypeAgent:
		if len(cur.Command) == 0 || cur.Command[0] == "" {
			v.addf(p+".command", "is required (argv array)")
		}
		v.versionRegex(p+".output_regex", cur.OutputRegex)
		if a.Agent == "" {
			v.addf(p+".type", "type agent requires the app's agent field")
		}
		v.forbidden(p+".url", cur.URL != "", "for type agent")
		v.forbidden(p+".json_path", cur.JSONPath != "", "for type agent")
		v.forbidden(p+".token", cur.Token.IsSet(), "for type agent")
	default:
		v.oneOf(p+".type", cur.Type, TypeAPI, TypeAgent)
	}
}

var httpMethods = []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch}

func (v *validator) update(p string, a *App, scriptsDir string) {
	u := a.Update
	switch u.Type {
	case TypeAPI:
		v.httpURL(p+".url", u.URL)
		v.oneOf(p+".method", u.Method, httpMethods...)
		v.authPair(p, u.AuthHeader, u.Token)
		v.forbidden(p+".script", u.Script != "", "for type api")
	case TypeScript:
		if a.Agent == "" {
			v.addf(p+".type", "type script requires the app's agent field")
		}
		v.script(p+".script", u.Script, scriptsDir)
		v.forbidden(p+".url", u.URL != "", "for type script")
		v.forbidden(p+".token", u.Token.IsSet(), "for type script")
		v.forbidden(p+".body", u.Body != "", "for type script")
	default:
		v.oneOf(p+".type", u.Type, TypeAPI, TypeScript)
	}
	if u.Timeout <= 0 {
		v.addf(p+".timeout", "must be positive")
	}
	if r := u.Resources; r != nil {
		if r.Cores < 0 {
			v.addf(p+".resources.cores", "must be positive")
		}
		if r.Memory != 0 && r.Memory < 16 {
			v.addf(p+".resources.memory", "must be at least 16 (MiB)")
		}
		if r.Cores == 0 && r.Memory == 0 {
			v.addf(p+".resources", "needs cores, memory or both")
		}
	}
}

// script checks a script name: a plain *.sh file name that exists in the scripts dir.
func (v *validator) script(p, name, dir string) {
	if !v.required(p, name) {
		return
	}
	if name != filepath.Base(name) || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".sh") {
		v.addf(p, "must be a plain *.sh file name from scripts.dir (got %q)", name)
		return
	}
	info, err := os.Stat(filepath.Join(dir, name))
	if err != nil || !info.Mode().IsRegular() {
		v.addf(p, "script %q not found in scripts.dir (%s)", name, dir)
	}
}

// authPair requires auth_header and token to be set together.
func (v *validator) authPair(p, header string, token Secret) {
	if header != "" && !token.IsSet() {
		v.addf(p+".token", "is required when auth_header is set")
	}
	if header == "" && token.IsSet() {
		v.addf(p+".auth_header", "is required when token is set")
	}
}
