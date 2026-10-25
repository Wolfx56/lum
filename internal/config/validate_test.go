package config

import (
	"errors"
	"strings"
	"testing"
)

// validConfig loads the spec example; each test case breaks one rule.
func validConfig(t *testing.T) *Config {
	t.Helper()
	cfg, err := Load(Options{Dir: "testdata/spec", LookupEnv: fakeEnv(specEnv)})
	if err != nil {
		t.Fatalf("spec example must be valid: %v", err)
	}
	return cfg
}

func TestValidateRules(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(c *Config)
		wantPath string
		wantMsg  string
	}{
		{"id not kebab-case", func(c *Config) { c.Apps[0].ID = "Sonarr_4" }, "apps[0].id", "kebab-case"},
		{"duplicate id", func(c *Config) { c.Apps[1].ID = "sonarr" }, "apps[1].id", "duplicate id"},
		{"missing id", func(c *Config) { c.Apps[0].ID = "" }, "apps[0].id", "is required"},
		{"lxc_id not positive", func(c *Config) { c.Apps[0].LXCID = 0 }, "apps[0].lxc_id", "positive"},
		{"script without agent", func(c *Config) { c.Apps[1].Agent = "" }, "apps[1].update.type", "requires the app's agent"},
		{"current agent without agent", func(c *Config) {
			c.Apps[1].Agent = ""
			c.Apps[1].Update = nil
		}, "apps[1].current.type", "requires the app's agent"},
		{"script missing", func(c *Config) { c.Apps[1].Update.Script = "absent.sh" }, "apps[1].update.script", "not found"},
		{"script path traversal", func(c *Config) { c.Apps[1].Update.Script = "../vaultwarden.sh" }, "apps[1].update.script", "plain *.sh"},
		{"script not .sh", func(c *Config) { c.Apps[1].Update.Script = "vaultwarden.py" }, "apps[1].update.script", "plain *.sh"},
		{"tag_regex without version group", func(c *Config) { c.Apps[0].Upstream.TagRegex = `^v(\d+)$` }, "apps[0].upstream.tag_regex", "named group"},
		{"tag_regex invalid", func(c *Config) { c.Apps[0].Upstream.TagRegex = `(` }, "apps[0].upstream.tag_regex", "invalid regex"},
		{"output_regex without version group", func(c *Config) { c.Apps[1].Current.OutputRegex = `\d+` }, "apps[1].current.output_regex", "named group"},
		{"bad repo", func(c *Config) { c.Apps[0].Upstream.Repo = "sonarr" }, "apps[0].upstream.repo", "owner/name"},
		{"unknown upstream type", func(c *Config) { c.Apps[0].Upstream.Type = "gitlab" }, "apps[0].upstream.type", "must be one of"},
		{"invalid json_path", func(c *Config) { c.Apps[0].Current.JSONPath = "$.[" }, "apps[0].current.json_path", "invalid JSONPath"},
		{"token without auth_header", func(c *Config) { c.Apps[0].Current.AuthHeader = "" }, "apps[0].current.auth_header", "required when token"},
		{"api current with command", func(c *Config) { c.Apps[0].Current.Command = []string{"x"} }, "apps[0].current.command", "not allowed"},
		{"unknown current type", func(c *Config) { c.Apps[0].Current.Type = "ssh" }, "apps[0].current.type", "must be one of"},
		{"bad update method", func(c *Config) { c.Apps[0].Update.Method = "DELETE" }, "apps[0].update.method", "must be one of"},
		{"relative update url", func(c *Config) { c.Apps[0].Update.URL = "/api/v3/command" }, "apps[0].update.url", "absolute http(s) URL"},
		{"healthcheck status", func(c *Config) { c.Apps[0].Healthcheck.ExpectStatus = 42 }, "apps[0].healthcheck.expect_status", "HTTP status"},
		{"retention", func(c *Config) { c.Apps[0].Retention = 0 }, "apps[0].retention", "at least 1"},
		{"empty resources", func(c *Config) { c.Apps[1].Update.Resources = &Resources{} }, "apps[1].update.resources", "cores, memory"},
		{"negative cores", func(c *Config) { c.Apps[1].Update.Resources = &Resources{Cores: -1} }, "apps[1].update.resources.cores", "positive"},
		{"tiny memory", func(c *Config) { c.Apps[1].Update.Resources = &Resources{Memory: 4} }, "apps[1].update.resources.memory", "at least 16"},
		{"auth mode", func(c *Config) { c.Auth.Mode = "basic" }, "auth.mode", "must be one of"},
		{"oidc secret missing", func(c *Config) { c.Auth.OIDC.ClientSecret = "" }, "auth.oidc.client_secret", "is required"},
		{"oidc without base_url", func(c *Config) { c.Server.BaseURL = "" }, "server.base_url", "required with auth.mode oidc"},
		{"session ttl", func(c *Config) { c.Auth.SessionTTL = 0 }, "auth.session_ttl", "must be positive"},
		{"cron", func(c *Config) { c.Scheduler.CheckCron = "every day" }, "scheduler.check_cron", "invalid cron"},
		{"concurrency", func(c *Config) { c.Scheduler.Concurrency = 0 }, "scheduler.concurrency", "at least 1"},
		{"log level", func(c *Config) { c.Server.LogLevel = "trace" }, "server.log_level", "must be one of"},
		{"notification event", func(c *Config) { c.Notifications[0].On = []string{"update"} }, "notifications[0].on[0]", "must be one of"},
		{"notification type", func(c *Config) { c.Notifications[0].Type = "email" }, "notifications[0].type", "must be one of"},
		{"telegram without chat_id", func(c *Config) { c.Notifications[0].Type = NotifyTelegram }, "notifications[0].chat_id", "is required"},
		{"chat_id on webhook", func(c *Config) { c.Notifications[1].ChatID = "123" }, "notifications[1].chat_id", "is not allowed"},
		{"enrollment", func(c *Config) { c.Agents.Enrollment = "auto" }, "agents.enrollment", "must be one of"},
		{"offline_after", func(c *Config) { c.Agents.OfflineAfter = c.Agents.HeartbeatInterval }, "agents.offline_after", "greater than"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig(t)
			tc.mutate(cfg)
			err := validate(cfg)
			var verr *ValidationError
			if !errors.As(err, &verr) {
				t.Fatalf("want *ValidationError, got %v", err)
			}
			for _, p := range verr.Problems {
				if p.Path == tc.wantPath && strings.Contains(p.Message, tc.wantMsg) {
					return
				}
			}
			t.Errorf("no problem %s ~ %q in:\n%v", tc.wantPath, tc.wantMsg, err)
		})
	}
}

func TestValidateReportsAllProblems(t *testing.T) {
	cfg := validConfig(t)
	cfg.Apps[0].LXCID = -1
	cfg.Apps[1].ID = "Bad ID"
	cfg.Scheduler.Concurrency = 0
	var verr *ValidationError
	if !errors.As(validate(cfg), &verr) || len(verr.Problems) != 3 {
		t.Fatalf("want 3 problems at once, got %v", verr)
	}
}

func TestValidateNeverEchoesSecretURL(t *testing.T) {
	cfg := validConfig(t)
	cfg.Notifications[1].URL = "not a url with test-hook-token"
	err := validate(cfg)
	if err == nil || strings.Contains(err.Error(), "test-hook-token") {
		t.Fatalf("validation error must not contain the secret value: %v", err)
	}
}

func TestValidateProxmox(t *testing.T) {
	base := func() *Config {
		c := globalDefaults()
		c.Auth.Mode = AuthNone
		return &c
	}
	ok := base()
	ok.Proxmox = Proxmox{URL: "https://pve.example.com:8006", TokenID: "lum@pve!lum", TokenSecret: "test-secret",
		Fingerprint: "AB:CD:EF:01:23:45:67:89:AB:CD:EF:01:23:45:67:89:AB:CD:EF:01:23:45:67:89:AB:CD:EF:01:23:45:67:89"}
	if err := validate(ok); err != nil {
		t.Fatalf("valid proxmox section rejected: %v", err)
	}
	if err := validate(base()); err != nil {
		t.Fatalf("proxmox is optional: %v", err)
	}
	cases := map[string]struct {
		p    Proxmox
		path string
	}{
		"token id":    {Proxmox{URL: "https://pve:8006", TokenID: "lum", TokenSecret: "x"}, "proxmox.token_id"},
		"no secret":   {Proxmox{URL: "https://pve:8006", TokenID: "lum@pve!lum"}, "proxmox.token_secret"},
		"fingerprint": {Proxmox{URL: "https://pve:8006", TokenID: "lum@pve!lum", TokenSecret: "x", Fingerprint: "AB:CD"}, "proxmox.fingerprint"},
		"url":         {Proxmox{URL: "pve:8006", TokenID: "lum@pve!lum", TokenSecret: "x"}, "proxmox.url"},
		"no url":      {Proxmox{TokenID: "lum@pve!lum"}, "proxmox.token_id"},
	}
	for name, tc := range cases {
		c := base()
		c.Proxmox = tc.p
		err := validate(c)
		var verr *ValidationError
		if !errors.As(err, &verr) || verr.Problems[0].Path != tc.path {
			t.Errorf("%s: err = %v, want a problem at %s", name, err, tc.path)
		}
	}
}
