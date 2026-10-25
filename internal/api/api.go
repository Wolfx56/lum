// Package api implements the REST API /api/v1 of Deliverables 1 and 2 (spec §11).
// Responses never contain secrets: tokens are omitted and URLs go through
// redact.URL. See doc/api.md.
package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Wolfx56/lum/internal/auth"
	"github.com/Wolfx56/lum/internal/checker"
	"github.com/Wolfx56/lum/internal/config"
	"github.com/Wolfx56/lum/internal/httperr"
	"github.com/Wolfx56/lum/internal/service"
	"github.com/Wolfx56/lum/internal/store"
	"github.com/Wolfx56/lum/internal/version"
)

// historyLimit is the number of checks returned by GET /apps/{id}.
const historyLimit = 50

// API holds the handlers.
type API struct {
	svc    *service.Service
	logger *slog.Logger
}

// Register mounts the routes on g (already protected by auth and CSRF middlewares).
func Register(g gin.IRouter, svc *service.Service, logger *slog.Logger) {
	a := &API{svc: svc, logger: logger}
	g.GET("/me", a.me)
	g.GET("/apps", a.listApps)
	g.GET("/apps/:id", a.getApp)
	g.POST("/apps/:id/check", a.checkApp)
	g.POST("/checks", a.checkAll)
	g.GET("/system/status", a.systemStatus)
	g.POST("/config/reload", a.reloadConfig)

	// Deliverable 2.
	g.POST("/apps/:id/update", a.startUpdate)
	g.GET("/jobs", a.listJobs)
	g.GET("/jobs/:id", a.getJob)
	g.GET("/jobs/:id/logs", a.jobLogs)
	g.POST("/jobs/:id/cancel", a.cancelJob)
	g.POST("/jobs/:id/rollback", a.rollbackJob)
	g.GET("/agents", a.listAgents)
	g.POST("/agents/:id/sync-scripts", a.syncAgentScripts)
	g.POST("/agents/:id/revoke", a.revokeAgent)
	g.DELETE("/agents/:id", a.deleteAgent)
	g.GET("/audit", a.listAudit)
}

func (a *API) me(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"user": auth.UserFrom(c), "auth_mode": a.svc.Config().Auth.Mode})
}

func (a *API) listApps(c *gin.Context) {
	ctx := c.Request.Context()
	states, err := a.svc.Store().AppStates(ctx)
	if err != nil {
		a.internal(c, err)
		return
	}
	active, err := a.svc.Store().ActiveJobs(ctx)
	if err != nil {
		a.internal(c, err)
		return
	}
	cfg := a.svc.Config()
	apps := make([]AppSummary, 0, len(cfg.Apps))
	for _, app := range cfg.Apps {
		st, ok := states[app.ID]
		apps = append(apps, withActiveJob(summary(app, st, ok), active))
	}
	c.JSON(http.StatusOK, gin.H{"apps": apps, "check_running": a.svc.CheckRunning()})
}

func (a *API) getApp(c *gin.Context) {
	app, ok := a.svc.Config().App(c.Param("id"))
	if !ok {
		httperr.Abort(c, http.StatusNotFound, "app_not_found", "unknown app: "+c.Param("id"))
		return
	}
	ctx := c.Request.Context()
	st, err := a.svc.Store().AppState(ctx, app.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		a.internal(c, err)
		return
	}
	checked := err == nil
	checks, err := a.svc.Store().Checks(ctx, app.ID, historyLimit)
	if err != nil {
		a.internal(c, err)
		return
	}
	active, err := a.svc.Store().ActiveJobs(ctx)
	if err != nil {
		a.internal(c, err)
		return
	}
	d := detail(app, st, checked, checks)
	d.AppSummary = withActiveJob(d.AppSummary, active)
	c.JSON(http.StatusOK, d)
}

func (a *API) checkApp(c *gin.Context) {
	id := c.Param("id")
	res, err := a.svc.CheckApp(c.Request.Context(), id)
	if errors.Is(err, checker.ErrUnknownApp) {
		httperr.Abort(c, http.StatusNotFound, "app_not_found", "unknown app: "+id)
		return
	}
	if err != nil {
		a.internal(c, err)
		return
	}
	a.audit(c, "app.check", id, res.Status)
	c.JSON(http.StatusOK, gin.H{"check": withoutNotes(res)})
}

func (a *API) checkAll(c *gin.Context) {
	results, err := a.svc.CheckAll(c.Request.Context())
	if errors.Is(err, service.ErrCheckRunning) {
		httperr.Abort(c, http.StatusConflict, "check_running", "a full check is already running")
		return
	}
	for i := range results {
		results[i] = withoutNotes(results[i])
	}
	a.audit(c, "checks.run", "", "")
	c.JSON(http.StatusOK, gin.H{"checks": results})
}

func (a *API) reloadConfig(c *gin.Context) {
	err := a.svc.Reload()
	details := ""
	if err != nil {
		details = "failed"
	}
	a.audit(c, "config.reload", "", details)
	if err != nil {
		problem := configProblem(err)
		httperr.AbortWithDetails(c, http.StatusUnprocessableEntity, "invalid_config",
			"configuration not reloaded, the previous one stays active: "+problem.Message, problem)
		return
	}
	loadedAt, _ := a.svc.ConfigStatus()
	c.JSON(http.StatusOK, gin.H{"loaded_at": loadedAt, "apps": len(a.svc.Config().Apps)})
}

func (a *API) systemStatus(c *gin.Context) {
	ctx := c.Request.Context()
	cfg := a.svc.Config()
	states, err := a.svc.Store().AppStates(ctx)
	if err != nil {
		a.internal(c, err)
		return
	}

	loadedAt, reloadErr := a.svc.ConfigStatus()
	cs := ConfigStatus{Valid: reloadErr == nil, LoadedAt: loadedAt}
	if reloadErr != nil {
		p := configProblem(reloadErr)
		cs.LastReloadError = &p
	}

	var lastCheck *time.Time
	var errorsList []AppError
	for _, app := range cfg.Apps {
		st, ok := states[app.ID]
		if !ok {
			continue
		}
		if lastCheck == nil || st.LastCheckAt.After(*lastCheck) {
			t := st.LastCheckAt
			lastCheck = &t
		}
		if st.Status == "error" {
			errorsList = append(errorsList, AppError{AppID: app.ID, Name: app.Name, Error: st.Error, At: st.LastCheckAt})
		}
	}

	c.JSON(http.StatusOK, SystemStatus{
		Version:  version.Version,
		AuthMode: cfg.Auth.Mode,
		Config:   cs,
		GitHub:   GitHubStatus{RateLimit: a.svc.RateLimit(), TokenConfigured: cfg.GitHub.Token.IsSet()},
		Scheduler: SchedulerStatus{
			CheckCron:    cfg.Scheduler.CheckCron,
			Jitter:       cfg.Scheduler.Jitter.String(),
			Concurrency:  cfg.Scheduler.Concurrency,
			NextCheck:    a.svc.NextCheck(),
			LastCheck:    lastCheck,
			CheckRunning: a.svc.CheckRunning(),
		},
		Apps:         a.svc.AppStatuses(),
		RecentErrors: nonNil(errorsList),
		Proxmox:      a.svc.Proxmox(ctx),
	})
}

// audit records a write action with the authenticated identity (spec §10).
func (a *API) audit(c *gin.Context, action, target, details string) {
	user := auth.UserFrom(c)
	// Detached context: the audit entry must be written even if the client left.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 5*time.Second)
	defer cancel()
	if err := a.svc.Store().Audit(ctx, store.AuditEntry{User: user.Username, Action: action, Target: target, Details: details}); err != nil {
		a.logger.Error("audit failed", "action", action, "error", err.Error())
	}
	a.logger.Info("audit", "user", user.Username, "action", action, "target", target)
}

func (a *API) internal(c *gin.Context, err error) {
	a.logger.Error("api error", "path", c.FullPath(), "error", err.Error())
	httperr.Abort(c, http.StatusInternalServerError, "internal", "internal error")
}

// configProblem describes a configuration error for the API.
func configProblem(err error) ConfigError {
	var verr *config.ValidationError
	var merr *config.MissingEnvError
	switch {
	case errors.As(err, &verr):
		return ConfigError{Kind: "validation", Message: "invalid configuration", Problems: verr.Problems}
	case errors.As(err, &merr):
		return ConfigError{Kind: "missing_env", Message: merr.Error(), MissingVars: merr.Vars()}
	default:
		return ConfigError{Kind: "read", Message: err.Error()}
	}
}

func withoutNotes(c store.Check) store.Check {
	c.ReleaseNotes = ""
	return c
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
