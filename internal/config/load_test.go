package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeEnv returns a lookup over a fixed map. Values are test placeholders, not real secrets.
func fakeEnv(vars map[string]string) LookupFunc {
	return func(name string) (string, bool) {
		v, ok := vars[name]
		return v, ok
	}
}

var specEnv = map[string]string{
	"OIDC_CLIENT_SECRET":     "test-oidc-secret",
	"GITHUB_TOKEN":           "test-github-token",
	"HA_WEBHOOK_URL":         "https://ha.example.com/api/webhook/test-hook",
	"AGENT_BOOTSTRAP_SECRET": "test-bootstrap",
	"SONARR_API_KEY":         "test-sonarr-key",
	"TAUTULLI_API_KEY":       "test-tautulli-key",
	"PROXMOX_TOKEN_SECRET":   "test-proxmox-secret",
}

func TestLoadSpecExample(t *testing.T) {
	cfg, err := Load(Options{Dir: "testdata/spec", LookupEnv: fakeEnv(specEnv)})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.GitHub.Token.Reveal() != "test-github-token" {
		t.Errorf("github.token not substituted: %q", cfg.GitHub.Token.Reveal())
	}
	if got := cfg.Notifications[1].URL.Reveal(); got != specEnv["HA_WEBHOOK_URL"] {
		t.Errorf("notifications[1].url = %q", got)
	}
	if cfg.Agents.HeartbeatInterval != time.Minute || cfg.Agents.OfflineAfter != 5*time.Minute {
		t.Errorf("durations not parsed: %+v", cfg.Agents)
	}

	sonarr, ok := cfg.App("sonarr")
	if !ok {
		t.Fatal("app sonarr not found")
	}
	if sonarr.Current.Token.Reveal() != "test-sonarr-key" || sonarr.Update.Token.Reveal() != "test-sonarr-key" {
		t.Error("sonarr tokens not substituted")
	}
	if sonarr.Update.Timeout != DefaultUpdateTimeout || sonarr.Retention != DefaultRetention {
		t.Errorf("sonarr defaults not applied: timeout=%v retention=%d", sonarr.Update.Timeout, sonarr.Retention)
	}

	vw, _ := cfg.App("vaultwarden")
	if got := strings.Join(vw.Current.Command, " "); got != "/opt/vaultwarden/vaultwarden --version" {
		t.Errorf("vaultwarden command = %q", got)
	}

	wantDir, _ := filepath.Abs("testdata/spec/scripts")
	if cfg.Scripts.Dir != wantDir {
		t.Errorf("scripts.dir = %q, want %q (relative to the config dir)", cfg.Scripts.Dir, wantDir)
	}
}

func TestLoadMinimalAppliesDefaults(t *testing.T) {
	cfg, err := Load(Options{Dir: "testdata/minimal", LookupEnv: fakeEnv(nil)})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	checks := map[string]bool{
		"server.listen":         cfg.Server.Listen == DefaultListen,
		"server.log_level":      cfg.Server.LogLevel == DefaultLogLevel,
		"scheduler.check_cron":  cfg.Scheduler.CheckCron == DefaultCheckCron,
		"scheduler.concurrency": cfg.Scheduler.Concurrency == DefaultConcurrency,
		"github.cache_ttl":      cfg.GitHub.CacheTTL == DefaultCacheTTL,
		"agents.enrollment":     cfg.Agents.Enrollment == EnrollmentOpen,
		"scripts.dir":           cfg.Scripts.Dir == DefaultScriptsDir,
		"apps[0].name":          cfg.Apps[0].Name == "tautulli",
		"apps[0].tag_regex":     cfg.Apps[0].Upstream.TagRegex == DefaultTagRegex,
	}
	for field, ok := range checks {
		if !ok {
			t.Errorf("default not applied for %s", field)
		}
	}
}

func TestLoadMissingEnvListsAllVariables(t *testing.T) {
	_, err := Load(Options{Dir: "testdata/spec", LookupEnv: fakeEnv(map[string]string{
		"OIDC_CLIENT_SECRET": "x",
		"HA_WEBHOOK_URL":     "https://ha.example.com/hook",
	})})
	var missing *MissingEnvError
	if !errors.As(err, &missing) {
		t.Fatalf("want *MissingEnvError, got %v", err)
	}
	want := []string{"AGENT_BOOTSTRAP_SECRET", "GITHUB_TOKEN", "SONARR_API_KEY"}
	if got := missing.Vars(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("Vars() = %v, want %v", got, want)
	}
	if !strings.Contains(err.Error(), "SONARR_API_KEY (apps[0].current.token, apps[0].update.token)") {
		t.Errorf("error does not locate the references: %v", err)
	}
}

func TestLoadEnvFile(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, ".env")
	content := `# comment
export GITHUB_TOKEN="from-file"
SONARR_API_KEY='file-sonarr'
OIDC_CLIENT_SECRET=file-oidc
HA_WEBHOOK_URL=https://ha.example.com/hook
AGENT_BOOTSTRAP_SECRET=file-bootstrap
`
	if err := os.WriteFile(envFile, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	// The process environment wins over the .env file.
	cfg, err := Load(Options{
		Dir:       "testdata/spec",
		EnvFile:   envFile,
		LookupEnv: fakeEnv(map[string]string{"GITHUB_TOKEN": "from-env"}),
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.GitHub.Token.Reveal(); got != "from-env" {
		t.Errorf("github.token = %q, want from-env (environment wins)", got)
	}
	if got := cfg.Apps[0].Current.Token.Reveal(); got != "file-sonarr" {
		t.Errorf("sonarr token = %q, want file-sonarr (quotes stripped)", got)
	}

	// A missing .env file is ignored.
	if _, err := Load(Options{Dir: "testdata/spec", EnvFile: filepath.Join(dir, "absent"), LookupEnv: fakeEnv(specEnv)}); err != nil {
		t.Errorf("missing env file should be ignored: %v", err)
	}
}

func TestLoadInvalidEnvFile(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(envFile, []byte("NOT A VALID LINE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(Options{Dir: "testdata/spec", EnvFile: envFile, LookupEnv: fakeEnv(specEnv)})
	if err == nil || !strings.Contains(err.Error(), ":1: expected KEY=VALUE") {
		t.Fatalf("want a located parse error, got %v", err)
	}
}

func TestSubstitutionInsideString(t *testing.T) {
	cfg := &Config{Server: Server{BaseURL: "https://${HOST}:${PORT}/lum"}}
	if err := substituteEnv(cfg, fakeEnv(map[string]string{"HOST": "lum.local", "PORT": "8443"})); err != nil {
		t.Fatal(err)
	}
	if cfg.Server.BaseURL != "https://lum.local:8443/lum" {
		t.Errorf("BaseURL = %q", cfg.Server.BaseURL)
	}
}

// writeConfig creates a config dir with the given files and returns its path.
func writeConfig(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	dir := writeConfig(t, map[string]string{
		ServerFile: "auth:\n  mode: none\nschedular:\n  check_cron: \"* * * * *\"\n",
		AppsFile:   "apps: []\n",
	})
	_, err := Load(Options{Dir: dir, LookupEnv: fakeEnv(nil)})
	if err == nil || !strings.Contains(err.Error(), "server.yaml") || !strings.Contains(err.Error(), "schedular") {
		t.Fatalf("want an unknown-field error naming the file and field, got %v", err)
	}
}

func TestLoadRejectsAppsInServerFile(t *testing.T) {
	dir := writeConfig(t, map[string]string{
		ServerFile: "auth:\n  mode: none\napps:\n  - id: x\n",
		AppsFile:   "apps: []\n",
	})
	if _, err := Load(Options{Dir: dir, LookupEnv: fakeEnv(nil)}); err == nil || !strings.Contains(err.Error(), "apps must be defined in apps.yaml") {
		t.Fatalf("got %v", err)
	}
}

func TestLoadMissingFile(t *testing.T) {
	dir := writeConfig(t, map[string]string{ServerFile: "auth:\n  mode: none\n"})
	if _, err := Load(Options{Dir: dir, LookupEnv: fakeEnv(nil)}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("want ErrNotExist for the missing apps.yaml, got %v", err)
	}
}

// The configs shipped in the repo must stay valid.
func TestRepoConfigs(t *testing.T) {
	t.Run("dev", func(t *testing.T) {
		env := fakeEnv(map[string]string{"SONARR_API_KEY": "x", "AGENT_BOOTSTRAP_SECRET": "x", "PROXMOX_TOKEN_SECRET": "x"})
		if _, err := Load(Options{Dir: "../../dev/config", LookupEnv: env}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("deploy", func(t *testing.T) {
		// scripts.dir is /data/scripts: the only acceptable problems are the absent scripts.
		_, err := Load(Options{Dir: "../../deploy/config", LookupEnv: fakeEnv(specEnv)})
		var verr *ValidationError
		if err == nil {
			return
		}
		if !errors.As(err, &verr) {
			t.Fatal(err)
		}
		for _, p := range verr.Problems {
			if !strings.HasSuffix(p.Path, ".update.script") {
				t.Fatal(err)
			}
		}
	})
}

func TestExplicitZeroOverridesDefault(t *testing.T) {
	dir := writeConfig(t, map[string]string{
		ServerFile: "auth:\n  mode: none\nscheduler:\n  jitter: 0s\ngithub:\n  cache_ttl: 0s\n",
		AppsFile:   "apps: []\n",
	})
	cfg, err := Load(Options{Dir: dir, LookupEnv: fakeEnv(nil)})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Scheduler.Jitter != 0 || cfg.GitHub.CacheTTL != 0 {
		t.Errorf("explicit 0s must be kept: jitter=%v cache_ttl=%v", cfg.Scheduler.Jitter, cfg.GitHub.CacheTTL)
	}
	if cfg.Scheduler.Concurrency != DefaultConcurrency || cfg.Scheduler.CheckCron != DefaultCheckCron {
		t.Errorf("absent fields of a present section must keep their default: %+v", cfg.Scheduler)
	}
}
