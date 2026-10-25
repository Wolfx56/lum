package api

import (
	"time"

	"github.com/Wolfx56/lum/internal/config"
	"github.com/Wolfx56/lum/internal/redact"
	"github.com/Wolfx56/lum/internal/semver"
	"github.com/Wolfx56/lum/internal/service"
	"github.com/Wolfx56/lum/internal/store"
	"github.com/Wolfx56/lum/internal/upstream"
)

// AppSummary is one dashboard row (GET /apps).
type AppSummary struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	LXCID          int        `json:"lxc_id"`
	Agent          string     `json:"agent,omitempty"`
	Status         string     `json:"status"`
	CurrentVersion string     `json:"current_version"`
	LatestVersion  string     `json:"latest_version"`
	LastCheckAt    *time.Time `json:"last_check_at"` // null: never checked
	ReleaseURL     string     `json:"release_url,omitempty"`
	Error          string     `json:"error,omitempty"`
	Repo           string     `json:"repo"`
	CurrentType    string     `json:"current_type"`
	UpdateType     string     `json:"update_type,omitempty"`
	ActiveJob      *JobRef    `json:"active_job"` // null: no job running for the app
}

// JobRef points to the active job of an app.
type JobRef struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

func withActiveJob(s AppSummary, active map[string]store.Job) AppSummary {
	if j, ok := active[s.ID]; ok {
		s.ActiveJob = &JobRef{ID: j.ID, Status: j.Status}
	}
	return s
}

func summary(app config.App, st store.AppState, checked bool) AppSummary {
	s := AppSummary{
		ID: app.ID, Name: app.Name, LXCID: app.LXCID, Agent: app.Agent,
		Status: string(semver.Unknown), Repo: app.Upstream.Repo, CurrentType: app.Current.Type,
	}
	if app.Update != nil {
		s.UpdateType = app.Update.Type
	}
	if checked {
		t := st.LastCheckAt
		s.Status, s.CurrentVersion, s.LatestVersion = st.Status, st.CurrentVersion, st.LatestVersion
		s.LastCheckAt, s.ReleaseURL, s.Error = &t, st.ReleaseURL, st.Error
	}
	return s
}

// AppDetail is GET /apps/{id}: the summary, the recipe without secrets, the
// release notes of the latest version and the check history.
type AppDetail struct {
	AppSummary
	Recipe       Recipe        `json:"recipe"`
	ReleaseNotes string        `json:"release_notes"` // markdown
	Checks       []store.Check `json:"checks"`        // most recent first, without notes
}

// Recipe is the app configuration as shown in the UI (no tokens, redacted URLs).
type Recipe struct {
	Upstream             RecipeUpstream     `json:"upstream"`
	Current              RecipeCurrent      `json:"current"`
	Update               *RecipeUpdate      `json:"update,omitempty"`
	Healthcheck          *RecipeHealthcheck `json:"healthcheck,omitempty"`
	SnapshotBeforeUpdate bool               `json:"snapshot_before_update"`
	RollbackOnFailure    bool               `json:"rollback_on_failure"`
	Retention            int                `json:"retention"`
}

// RecipeUpstream mirrors config.Upstream.
type RecipeUpstream struct {
	Type              string `json:"type"`
	Repo              string `json:"repo"`
	IncludePrerelease bool   `json:"include_prerelease"`
	TagRegex          string `json:"tag_regex"`
}

// RecipeCurrent mirrors config.Current without the token.
type RecipeCurrent struct {
	Type        string   `json:"type"`
	URL         string   `json:"url,omitempty"`
	AuthHeader  string   `json:"auth_header,omitempty"`
	JSONPath    string   `json:"json_path,omitempty"`
	Command     []string `json:"command,omitempty"`
	OutputRegex string   `json:"output_regex,omitempty"`
}

// RecipeUpdate mirrors config.Update without the token and body.
type RecipeUpdate struct {
	Type      string           `json:"type"`
	Method    string           `json:"method,omitempty"`
	URL       string           `json:"url,omitempty"`
	Script    string           `json:"script,omitempty"`
	Timeout   string           `json:"timeout"`
	Resources *RecipeResources `json:"resources,omitempty"`
}

// RecipeResources mirrors config.Resources (memory in MiB, 0 = not set).
type RecipeResources struct {
	Cores  int `json:"cores,omitempty"`
	Memory int `json:"memory,omitempty"`
}

// RecipeHealthcheck mirrors config.Healthcheck.
type RecipeHealthcheck struct {
	URL          string `json:"url"`
	ExpectStatus int    `json:"expect_status"`
	Timeout      string `json:"timeout"`
}

func redactOptional(u string) string {
	if u == "" {
		return ""
	}
	return redact.URL(u)
}

func detail(app config.App, st store.AppState, checked bool, checks []store.Check) AppDetail {
	d := AppDetail{
		AppSummary: summary(app, st, checked),
		Recipe: Recipe{
			Upstream: RecipeUpstream{
				Type: app.Upstream.Type, Repo: app.Upstream.Repo,
				IncludePrerelease: app.Upstream.IncludePrerelease, TagRegex: app.Upstream.TagRegex,
			},
			Current: RecipeCurrent{
				Type: app.Current.Type, URL: redactOptional(app.Current.URL), AuthHeader: app.Current.AuthHeader,
				JSONPath: app.Current.JSONPath, Command: app.Current.Command, OutputRegex: app.Current.OutputRegex,
			},
			SnapshotBeforeUpdate: app.SnapshotBeforeUpdate,
			RollbackOnFailure:    app.RollbackOnFailure,
			Retention:            app.Retention,
		},
		Checks: make([]store.Check, 0, len(checks)),
	}
	if u := app.Update; u != nil {
		d.Recipe.Update = &RecipeUpdate{Type: u.Type, Method: u.Method, URL: redactOptional(u.URL), Script: u.Script, Timeout: u.Timeout.String()}
		if r := u.Resources; r != nil {
			d.Recipe.Update.Resources = &RecipeResources{Cores: r.Cores, Memory: r.Memory}
		}
	}
	if h := app.Healthcheck; h != nil {
		d.Recipe.Healthcheck = &RecipeHealthcheck{URL: redact.URL(h.URL), ExpectStatus: h.ExpectStatus, Timeout: h.Timeout.String()}
	}
	for _, c := range checks {
		if d.ReleaseNotes == "" && c.ReleaseNotes != "" && c.LatestVersion == d.LatestVersion {
			d.ReleaseNotes = c.ReleaseNotes
		}
		d.Checks = append(d.Checks, withoutNotes(c))
	}
	return d
}

// SystemStatus is GET /system/status.
type SystemStatus struct {
	Version      string                `json:"version"`
	AuthMode     string                `json:"auth_mode"`
	Config       ConfigStatus          `json:"config"`
	GitHub       GitHubStatus          `json:"github"`
	Scheduler    SchedulerStatus       `json:"scheduler"`
	Apps         map[string]int        `json:"apps"` // status → count
	RecentErrors []AppError            `json:"recent_errors"`
	Proxmox      service.ProxmoxStatus `json:"proxmox"`
}

// ConfigStatus reports the active configuration and the last reload attempt.
type ConfigStatus struct {
	Valid           bool         `json:"valid"` // false: the last reload failed (the previous config is active)
	LoadedAt        time.Time    `json:"loaded_at"`
	LastReloadError *ConfigError `json:"last_reload_error,omitempty"`
}

// ConfigError describes why a configuration could not be loaded.
type ConfigError struct {
	Kind        string           `json:"kind"` // validation | missing_env | read
	Message     string           `json:"message"`
	Problems    []config.Problem `json:"problems,omitempty"`
	MissingVars []string         `json:"missing_vars,omitempty"`
}

// GitHubStatus reports the GitHub quota.
type GitHubStatus struct {
	upstream.RateLimit
	TokenConfigured bool `json:"token_configured"`
}

// SchedulerStatus reports the check schedule.
type SchedulerStatus struct {
	CheckCron    string     `json:"check_cron"`
	Jitter       string     `json:"jitter"`
	Concurrency  int        `json:"concurrency"`
	NextCheck    time.Time  `json:"next_check"`
	LastCheck    *time.Time `json:"last_check"`
	CheckRunning bool       `json:"check_running"`
}

// AppError is an app currently in error.
type AppError struct {
	AppID string    `json:"app_id"`
	Name  string    `json:"name"`
	Error string    `json:"error"`
	At    time.Time `json:"at"`
}
