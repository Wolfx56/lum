package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Wolfx56/lum/internal/agenthub"
	"github.com/Wolfx56/lum/internal/auth"
	"github.com/Wolfx56/lum/internal/current"
	"github.com/Wolfx56/lum/internal/httperr"
	"github.com/Wolfx56/lum/internal/jobs"
	"github.com/Wolfx56/lum/internal/store"
)

// Job list limits (GET /jobs).
const (
	defaultJobsLimit = 50
	maxJobsLimit     = 500
)

// SSE tuning (GET /jobs/{id}/logs).
const (
	sseKeepAlive = 15 * time.Second       // comment line sent to keep proxies from closing the stream
	sseCoalesce  = 250 * time.Millisecond // lines written meanwhile are sent together
)

// JobView is a job as returned by the API.
type JobView struct {
	store.Job
	AppName string `json:"app_name"`
	LXCID   int    `json:"lxc_id,omitempty"` // 0: app no longer in apps.yaml
}

// JobDetail is GET /jobs/{id}.
type JobDetail struct {
	Job JobView `json:"job"`
	// Running: the job runs in this server process and can be cancelled.
	Running  bool          `json:"running"`
	Rollback RollbackState `json:"rollback"`
}

// RollbackState tells whether POST /jobs/{id}/rollback would be accepted.
type RollbackState struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`  // error code when not available
	Message   string `json:"message,omitempty"` // human detail of the reason
}

func (a *API) jobView(j store.Job) JobView {
	v := JobView{Job: j, AppName: j.AppID}
	if app, ok := a.svc.Config().App(j.AppID); ok {
		v.AppName, v.LXCID = app.Name, app.LXCID
	}
	return v
}

// jobError maps the errors of the job manager to the uniform API errors.
func jobError(err error) (status int, code string) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return http.StatusNotFound, "job_not_found"
	case errors.Is(err, jobs.ErrUnknownApp):
		return http.StatusNotFound, "app_not_found"
	case errors.Is(err, jobs.ErrNoUpdate):
		return http.StatusUnprocessableEntity, "no_update_recipe"
	case errors.Is(err, jobs.ErrUnsupported):
		return http.StatusUnprocessableEntity, "unsupported_update"
	case errors.Is(err, jobs.ErrProxmoxMissing):
		return http.StatusUnprocessableEntity, "proxmox_not_configured"
	case errors.Is(err, jobs.ErrNotUpdatable):
		return http.StatusConflict, "no_update_available"
	case errors.Is(err, jobs.ErrRollbackRunning):
		return http.StatusConflict, "rollback_running"
	case errors.Is(err, jobs.ErrJobActive):
		return http.StatusConflict, "job_active"
	case errors.Is(err, jobs.ErrNotRunning):
		return http.StatusConflict, "job_not_running"
	case errors.Is(err, jobs.ErrNoSnapshot):
		return http.StatusConflict, "no_snapshot"
	case errors.Is(err, jobs.ErrJobNotFinished):
		return http.StatusConflict, "job_not_finished"
	case errors.Is(err, jobs.ErrAlreadyRolledBack):
		return http.StatusConflict, "already_rolled_back"
	case errors.Is(err, jobs.ErrSnapshotGone):
		return http.StatusConflict, "snapshot_gone"
	case errors.Is(err, jobs.ErrAgentOffline), errors.Is(err, current.ErrAgentUnavailable):
		return http.StatusServiceUnavailable, "agent_offline"
	case errors.Is(err, agenthub.ErrScriptNotSynced):
		return http.StatusServiceUnavailable, "script_not_synced"
	case errors.Is(err, jobs.ErrProxmox):
		return http.StatusBadGateway, "proxmox_error"
	}
	return http.StatusInternalServerError, "internal"
}

// abortJobError writes a job error; 409 job_active carries the active job id.
func (a *API) abortJobError(c *gin.Context, err error, appID string) string {
	status, code := jobError(err)
	if code == "internal" {
		a.internal(c, err)
		return code
	}
	if code == "job_active" && appID != "" {
		if active, aerr := a.svc.Store().ActiveJobs(c.Request.Context()); aerr == nil {
			if j, ok := active[appID]; ok {
				httperr.AbortWithDetails(c, status, code, err.Error(), gin.H{"job_id": j.ID})
				return code
			}
		}
	}
	httperr.Abort(c, status, code, err.Error())
	return code
}

// startUpdate is POST /apps/{id}/update (spec §7, 409 if a job is active).
func (a *API) startUpdate(c *gin.Context) {
	appID := c.Param("id")
	job, err := a.svc.Jobs().Start(c.Request.Context(), appID, auth.UserFrom(c).Username)
	if err != nil {
		code := a.abortJobError(c, err, appID)
		a.audit(c, "app.update", appID, "refused: "+code)
		return
	}
	a.audit(c, "app.update", appID, "job "+job.ID)
	c.JSON(http.StatusAccepted, gin.H{"job": a.jobView(job)})
}

// listJobs is GET /jobs?app=&status=&limit= (status: comma-separated, or "active").
func (a *API) listJobs(c *gin.Context) {
	f := store.JobFilter{AppID: c.Query("app"), Limit: defaultJobsLimit}
	if s := c.Query("status"); s != "" {
		for st := range strings.SplitSeq(s, ",") {
			if st == "active" {
				f.Statuses = append(f.Statuses, store.ActiveJobStates...)
			} else if st != "" {
				f.Statuses = append(f.Statuses, st)
			}
		}
	}
	if l := c.Query("limit"); l != "" {
		n, err := strconv.Atoi(l)
		if err != nil || n < 1 || n > maxJobsLimit {
			httperr.Abort(c, http.StatusBadRequest, "invalid_limit", fmt.Sprintf("limit must be between 1 and %d", maxJobsLimit))
			return
		}
		f.Limit = n
	}
	list, err := a.svc.Store().ListJobs(c.Request.Context(), f)
	if err != nil {
		a.internal(c, err)
		return
	}
	out := make([]JobView, 0, len(list))
	for _, j := range list {
		out = append(out, a.jobView(j))
	}
	c.JSON(http.StatusOK, gin.H{"jobs": out})
}

// getJob is GET /jobs/{id}.
func (a *API) getJob(c *gin.Context) {
	ctx := c.Request.Context()
	job, err := a.svc.Store().Job(ctx, c.Param("id"))
	if err != nil {
		a.abortJobError(c, err, "")
		return
	}
	d := JobDetail{Job: a.jobView(job), Running: a.svc.Jobs().Running(job.ID)}
	// May list the container snapshots: bounded so that the page stays responsive.
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := a.svc.Jobs().RollbackCheck(cctx, job); err != nil {
		_, code := jobError(err)
		d.Rollback = RollbackState{Reason: code, Message: err.Error()}
	} else {
		d.Rollback.Available = true
	}
	c.JSON(http.StatusOK, d)
}

// cancelJob is POST /jobs/{id}/cancel.
func (a *API) cancelJob(c *gin.Context) {
	ctx := c.Request.Context()
	id := c.Param("id")
	job, err := a.svc.Store().Job(ctx, id)
	if err != nil {
		a.abortJobError(c, err, "")
		return
	}
	if err := a.svc.Jobs().Cancel(ctx, id, auth.UserFrom(c).Username); err != nil {
		if errors.Is(err, jobs.ErrNotRunning) {
			a.abortJobError(c, err, "")
		} else {
			// cancel_job not delivered to the agent: the job goes on.
			httperr.Abort(c, http.StatusBadGateway, "agent_error", err.Error())
		}
		a.audit(c, "job.cancel", id, "refused: "+job.AppID)
		return
	}
	a.audit(c, "job.cancel", id, job.AppID)
	if job, err = a.svc.Store().Job(ctx, id); err != nil {
		a.internal(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"job": a.jobView(job)})
}

// rollbackJob is POST /jobs/{id}/rollback: restores the job's snapshot.
func (a *API) rollbackJob(c *gin.Context) {
	ctx := c.Request.Context()
	id := c.Param("id")
	job, err := a.svc.Store().Job(ctx, id)
	if err != nil {
		a.abortJobError(c, err, "")
		return
	}
	if _, err := a.svc.Jobs().Rollback(ctx, id, auth.UserFrom(c).Username); err != nil {
		code := a.abortJobError(c, err, job.AppID)
		a.audit(c, "job.rollback", id, "refused: "+code)
		return
	}
	a.audit(c, "job.rollback", id, job.AppID+" "+job.SnapshotName)
	c.JSON(http.StatusAccepted, gin.H{"job": a.jobView(job)})
}

// jobLogs is GET /jobs/{id}/logs: the job logs as Server-Sent Events.
//
//	event: log     id = seq, data = {"seq", "ts", "stream", "line"}
//	event: status  data = the job, at the start and on every status change
//	event: end     data = the final job; the server then closes the stream
//
// The stream resumes after Last-Event-ID (EventSource reconnection) or ?after=seq.
func (a *API) jobLogs(c *gin.Context) {
	ctx := c.Request.Context()
	id := c.Param("id")
	st, mgr := a.svc.Store(), a.svc.Jobs()
	job, err := st.Job(ctx, id)
	if err != nil {
		a.abortJobError(c, err, "")
		return
	}
	after := int64(0)
	for _, v := range []string{c.GetHeader("Last-Event-ID"), c.Query("after")} {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > after {
			after = n
		}
	}

	w := c.Writer
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no") // no buffering by a reverse proxy
	w.WriteHeader(http.StatusOK)
	w.Flush()

	keepAlive := time.NewTicker(sseKeepAlive)
	defer keepAlive.Stop()
	lastStatus := ""
	for {
		// Subscribe before reading, so that no change is missed in between.
		// nil: the job no longer changes (finished, or interrupted by a restart).
		wake := mgr.Watch(id)
		if job, err = st.Job(ctx, id); err != nil {
			return
		}
		logs, err := st.JobLogs(ctx, id, after)
		if err != nil {
			return
		}
		for _, l := range logs {
			if writeEvent(w, "log", strconv.FormatInt(l.Seq, 10), l) != nil {
				return
			}
			after = l.Seq
		}
		if job.Status != lastStatus {
			if writeEvent(w, "status", "", a.jobView(job)) != nil {
				return
			}
			lastStatus = job.Status
		}
		if wake == nil {
			_ = writeEvent(w, "end", "", a.jobView(job))
			w.Flush()
			return
		}
		w.Flush()

		select {
		case <-ctx.Done():
			return
		case <-keepAlive.C:
			if _, err := w.WriteString(": keep-alive\n\n"); err != nil {
				return
			}
			w.Flush()
		case <-wake:
			select { // let a burst of lines accumulate
			case <-ctx.Done():
				return
			case <-time.After(sseCoalesce):
			}
		}
	}
}

func writeEvent(w gin.ResponseWriter, event, id string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	var sb strings.Builder
	if id != "" {
		sb.WriteString("id: " + id + "\n")
	}
	sb.WriteString("event: " + event + "\ndata: ")
	sb.Write(b)
	sb.WriteString("\n\n")
	_, err = w.WriteString(sb.String())
	return err
}
