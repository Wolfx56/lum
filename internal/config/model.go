// Package config loads, validates and holds the LUM configuration
// (config/server.yaml + config/apps.yaml, spec §4). See doc/configuration.md.
package config

import "time"

// Config is the whole configuration: server.yaml fields plus the apps from apps.yaml.
type Config struct {
	Server        Server         `yaml:"server"`
	Auth          Auth           `yaml:"auth"`
	GitHub        GitHub         `yaml:"github"`
	Scheduler     Scheduler      `yaml:"scheduler"`
	Notifications []Notification `yaml:"notifications"`
	Agents        Agents         `yaml:"agents"`
	Scripts       Scripts        `yaml:"scripts"`
	Proxmox       Proxmox        `yaml:"proxmox"`
	// Apps comes from apps.yaml; defining it in server.yaml is an error.
	Apps []App `yaml:"apps"`
}

// Server holds the HTTP server settings.
type Server struct {
	Listen   string `yaml:"listen"`
	BaseURL  string `yaml:"base_url"`
	LogLevel string `yaml:"log_level"`
}

// Auth modes.
const (
	AuthOIDC = "oidc"
	AuthNone = "none"
)

// Auth configures user authentication on the UI and API.
type Auth struct {
	Mode string `yaml:"mode"`
	OIDC OIDC   `yaml:"oidc"`
	// SessionTTL is the absolute lifetime of a UI session (cookie).
	SessionTTL time.Duration `yaml:"session_ttl"`
}

// OIDC holds the OpenID Connect client settings (Keycloak).
type OIDC struct {
	Issuer       string `yaml:"issuer"`
	ClientID     string `yaml:"client_id"`
	ClientSecret Secret `yaml:"client_secret"`
}

// GitHub configures the GitHub API client.
type GitHub struct {
	Token    Secret        `yaml:"token"`
	CacheTTL time.Duration `yaml:"cache_ttl"`
}

// Scheduler configures the periodic version checks.
type Scheduler struct {
	CheckCron   string        `yaml:"check_cron"`
	Jitter      time.Duration `yaml:"jitter"`
	Concurrency int           `yaml:"concurrency"`
}

// Notification events.
const (
	EventUpdateAvailable = "update_available"
	EventJobSuccess      = "job_success"
	EventJobFailed       = "job_failed"
	EventAgentOffline    = "agent_offline"
)

// Notification channel types.
const (
	NotifyNtfy     = "ntfy"
	NotifyWebhook  = "webhook"
	NotifyTelegram = "telegram"
)

// Notification is one notification channel.
type Notification struct {
	Type string `yaml:"type"`
	// URL is a Secret because webhook URLs often embed a token.
	URL Secret   `yaml:"url"`
	On  []string `yaml:"on"`
	// ChatID is the Telegram recipient (type telegram only): numeric id, "-100…" or "@channel".
	ChatID string `yaml:"chat_id"`
}

// Agent enrollment modes.
const (
	EnrollmentOpen   = "open"
	EnrollmentClosed = "closed"
)

// Agent self-update modes.
const (
	SelfUpdateAuto = "auto" // agents whose binary differs from the server's are updated
	SelfUpdateOff  = "off"
)

// Agents configures the agent hub.
type Agents struct {
	SharedSecret      Secret        `yaml:"shared_secret"`
	HeartbeatInterval time.Duration `yaml:"heartbeat_interval"`
	OfflineAfter      time.Duration `yaml:"offline_after"`
	Enrollment        string        `yaml:"enrollment"`
	SelfUpdate        string        `yaml:"self_update"`
}

// Scripts configures the central update-script repository.
type Scripts struct {
	// Dir is resolved to an absolute path; a relative value is relative to the config directory.
	Dir string `yaml:"dir"`
}

// Proxmox configures the Proxmox VE API used for the pre-update snapshots
// (REST API with an API token instead of the spec's SSH, see DECISIONS.md D16).
type Proxmox struct {
	// URL of the API, e.g. https://pve.example.com:8006 (one node of the cluster).
	URL string `yaml:"url"`
	// TokenID is the API token id: user@realm!name.
	TokenID     string `yaml:"token_id"`
	TokenSecret Secret `yaml:"token_secret"`
	// Fingerprint pins the SHA-256 fingerprint of the server certificate (the
	// self-signed certificate of a node). Empty: the system CAs are used.
	Fingerprint string `yaml:"fingerprint"`
}

// Configured reports whether a Proxmox API is configured.
func (p Proxmox) Configured() bool { return p.URL != "" }

// App is one application recipe from apps.yaml.
type App struct {
	ID                   string       `yaml:"id"`
	Name                 string       `yaml:"name"`
	LXCID                int          `yaml:"lxc_id"`
	Agent                string       `yaml:"agent"`
	Upstream             Upstream     `yaml:"upstream"`
	Current              Current      `yaml:"current"`
	Update               *Update      `yaml:"update"`
	Healthcheck          *Healthcheck `yaml:"healthcheck"`
	SnapshotBeforeUpdate bool         `yaml:"snapshot_before_update"`
	RollbackOnFailure    bool         `yaml:"rollback_on_failure"`
	// Retention is the number of successful pre-update snapshots kept.
	Retention int `yaml:"retention"`
}

// UpstreamGitHub is the only upstream source type (spec §5.2).
const UpstreamGitHub = "github"

// Upstream describes where the latest available version comes from.
type Upstream struct {
	Type              string `yaml:"type"`
	Repo              string `yaml:"repo"`
	IncludePrerelease bool   `yaml:"include_prerelease"`
	// TagRegex must contain the named group "version".
	TagRegex string `yaml:"tag_regex"`
}

// Current (installed version) and Update types.
const (
	TypeAPI    = "api"
	TypeAgent  = "agent"
	TypeScript = "script"
)

// Current describes how to read the installed version (type api or agent).
type Current struct {
	Type string `yaml:"type"`

	// type: api
	URL        string `yaml:"url"`
	AuthHeader string `yaml:"auth_header"`
	Token      Secret `yaml:"token"`
	JSONPath   string `yaml:"json_path"`

	// type: agent
	Command []string `yaml:"command"`
	// OutputRegex must contain the named group "version".
	OutputRegex string `yaml:"output_regex"`
}

// Update describes how to update the app (type api or script, Deliverable 2).
type Update struct {
	Type string `yaml:"type"`

	// type: api
	Method     string `yaml:"method"`
	URL        string `yaml:"url"`
	AuthHeader string `yaml:"auth_header"`
	Token      Secret `yaml:"token"`
	Body       string `yaml:"body"`

	// type: script
	Script string `yaml:"script"`

	Timeout time.Duration `yaml:"timeout"`
	// Resources raises the container's CPU and memory for the update (D19).
	Resources *Resources `yaml:"resources"`
}

// Resources is the minimum the container gets during an update: LUM raises
// what is below it through the Proxmox API, then restores the previous values.
type Resources struct {
	Cores  int `yaml:"cores"`
	Memory int `yaml:"memory"` // MiB
}

// Healthcheck is polled after an update until ExpectStatus or Timeout.
type Healthcheck struct {
	URL          string        `yaml:"url"`
	ExpectStatus int           `yaml:"expect_status"`
	Timeout      time.Duration `yaml:"timeout"`
}

// NeedsProxmox reports whether updating the app uses the Proxmox API
// (snapshot or update.resources).
func (a App) NeedsProxmox() bool {
	return a.SnapshotBeforeUpdate || (a.Update != nil && a.Update.Resources != nil)
}

// App returns the app with the given id.
func (c *Config) App(id string) (App, bool) {
	for _, a := range c.Apps {
		if a.ID == id {
			return a, true
		}
	}
	return App{}, false
}
