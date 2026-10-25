package api

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Wolfx56/lum/internal/current"
	"github.com/Wolfx56/lum/internal/httperr"
	"github.com/Wolfx56/lum/internal/store"
)

// Agent states shown by the API.
const (
	agentOnline      = "online"
	agentOffline     = "offline"
	agentRevoked     = "revoked"
	agentNotEnrolled = "not_enrolled" // referenced by a recipe, never enrolled
)

// Script sync states (spec §9).
const (
	scriptInSync  = "in_sync"
	scriptStale   = "stale"
	scriptMissing = "missing"
)

// Audit log limits (GET /audit).
const (
	defaultAuditLimit = 100
	maxAuditLimit     = 1000
)

// AgentView is one agent of GET /agents.
type AgentView struct {
	ID           string       `json:"id"`
	Status       string       `json:"status"` // online | offline | revoked | not_enrolled
	AgentVersion string       `json:"agent_version,omitempty"`
	Hostname     string       `json:"hostname,omitempty"`
	LastSeenAt   *time.Time   `json:"last_seen_at"`
	EnrolledAt   *time.Time   `json:"enrolled_at"`
	Binary       string       `json:"binary"` // up_to_date | outdated | unknown
	Apps         []string     `json:"apps"`   // apps of apps.yaml using this agent
	Scripts      []ScriptSync `json:"scripts"`
}

// ScriptSync is the state of one script the agent must hold.
type ScriptSync struct {
	Name         string `json:"name"`
	State        string `json:"state"` // in_sync | stale | missing
	ServerSHA256 string `json:"server_sha256"`
	AgentSHA256  string `json:"agent_sha256,omitempty"`
}

func optionalTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// agentView builds the view of an agent; enrolled is false for an agent only
// referenced by apps.yaml.
func (a *API) agentView(ctx context.Context, ag store.Agent, enrolled bool) (AgentView, error) {
	hub := a.svc.Hub()
	v := AgentView{ID: ag.ID, Apps: []string{}, Scripts: []ScriptSync{}, Binary: hub.BinaryStatus(ag.ID)}
	switch {
	case !enrolled:
		v.Status = agentNotEnrolled
	case ag.Revoked:
		v.Status = agentRevoked
	case hub.Connected(ag.ID):
		v.Status = agentOnline
	default:
		v.Status = agentOffline
	}
	if enrolled {
		v.AgentVersion, v.Hostname = ag.AgentVersion, ag.Hostname
		v.LastSeenAt, v.EnrolledAt = optionalTime(ag.LastSeenAt), optionalTime(ag.EnrolledAt)
	}
	for _, app := range a.svc.Config().Apps {
		if app.Agent == ag.ID {
			v.Apps = append(v.Apps, app.ID)
		}
	}

	reported := map[string]string{}
	if enrolled {
		var err error
		if reported, err = a.svc.Store().AgentScripts(ctx, ag.ID); err != nil {
			return AgentView{}, err
		}
	}
	desired := hub.Desired(ag.ID)
	for _, name := range slices.Sorted(maps.Keys(desired)) {
		s := ScriptSync{Name: name, ServerSHA256: desired[name].SHA256, AgentSHA256: reported[name]}
		switch s.AgentSHA256 {
		case "":
			s.State = scriptMissing
		case s.ServerSHA256:
			s.State = scriptInSync
		default:
			s.State = scriptStale
		}
		v.Scripts = append(v.Scripts, s)
	}
	return v, nil
}

// findAgent returns an enrolled agent, or one referenced by apps.yaml only
// (enrolled false); store.ErrNotFound otherwise.
func (a *API) findAgent(ctx context.Context, id string) (store.Agent, bool, error) {
	ag, err := a.svc.Store().Agent(ctx, id)
	if err == nil {
		return ag, true, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return store.Agent{}, false, err
	}
	for _, app := range a.svc.Config().Apps {
		if app.Agent == id {
			return store.Agent{ID: id}, false, nil
		}
	}
	return store.Agent{}, false, store.ErrNotFound
}

// listAgents is GET /agents: the enrolled agents and those referenced by
// apps.yaml but never enrolled.
func (a *API) listAgents(c *gin.Context) {
	ctx := c.Request.Context()
	enrolled, err := a.svc.Store().Agents(ctx)
	if err != nil {
		a.internal(c, err)
		return
	}
	known := map[string]bool{}
	out := make([]AgentView, 0, len(enrolled))
	for _, ag := range enrolled {
		known[ag.ID] = true
		v, err := a.agentView(ctx, ag, true)
		if err != nil {
			a.internal(c, err)
			return
		}
		out = append(out, v)
	}
	for _, app := range a.svc.Config().Apps {
		if app.Agent == "" || known[app.Agent] {
			continue
		}
		known[app.Agent] = true
		v, err := a.agentView(ctx, store.Agent{ID: app.Agent}, false)
		if err != nil {
			a.internal(c, err)
			return
		}
		out = append(out, v)
	}
	slices.SortFunc(out, func(x, y AgentView) int { return cmp.Compare(x.ID, y.ID) })
	c.JSON(http.StatusOK, gin.H{"agents": out})
}

// agentFor loads the agent of the route, writing 404 if unknown.
func (a *API) agentFor(c *gin.Context) (store.Agent, bool, bool) {
	ag, enrolled, err := a.findAgent(c.Request.Context(), c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		httperr.Abort(c, http.StatusNotFound, "agent_not_found", "unknown agent: "+c.Param("id"))
		return store.Agent{}, false, false
	}
	if err != nil {
		a.internal(c, err)
		return store.Agent{}, false, false
	}
	return ag, enrolled, true
}

func (a *API) respondAgent(c *gin.Context, id string) {
	ctx := c.Request.Context()
	ag, enrolled, err := a.findAgent(ctx, id)
	if err != nil {
		a.internal(c, err)
		return
	}
	v, err := a.agentView(ctx, ag, enrolled)
	if err != nil {
		a.internal(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"agent": v})
}

// syncAgentScripts is POST /agents/{id}/sync-scripts.
func (a *API) syncAgentScripts(c *gin.Context) {
	ag, enrolled, ok := a.agentFor(c)
	if !ok {
		return
	}
	if ag.Revoked {
		httperr.Abort(c, http.StatusConflict, "agent_revoked", "agent "+ag.ID+" is revoked")
		a.audit(c, "agent.sync_scripts", ag.ID, "refused: agent_revoked")
		return
	}
	_, err := a.svc.Hub().SyncScripts(c.Request.Context(), ag.ID)
	switch {
	case !enrolled, errors.Is(err, current.ErrAgentUnavailable):
		httperr.Abort(c, http.StatusServiceUnavailable, "agent_offline", "agent "+ag.ID+" is not connected")
		a.audit(c, "agent.sync_scripts", ag.ID, "refused: agent_offline")
		return
	case err != nil:
		httperr.Abort(c, http.StatusBadGateway, "agent_error", err.Error())
		a.audit(c, "agent.sync_scripts", ag.ID, "failed")
		return
	}
	a.audit(c, "agent.sync_scripts", ag.ID, "")
	a.respondAgent(c, ag.ID)
}

// revokeAgent is POST /agents/{id}/revoke: the agent token is refused from now
// on, and its connection is closed (spec §10).
func (a *API) revokeAgent(c *gin.Context) {
	ag, enrolled, ok := a.agentFor(c)
	if !ok {
		return
	}
	if !enrolled {
		httperr.Abort(c, http.StatusConflict, "agent_not_enrolled", "agent "+ag.ID+" has never enrolled: nothing to revoke")
		return
	}
	if err := a.svc.Store().RevokeAgent(c.Request.Context(), ag.ID); err != nil {
		a.internal(c, err)
		return
	}
	a.svc.Hub().Disconnect(ag.ID)
	a.audit(c, "agent.revoke", ag.ID, "")
	a.respondAgent(c, ag.ID)
}

// deleteAgent is DELETE /agents/{id}: forgets a revoked agent, which can then
// enroll again with the shared secret (D18).
func (a *API) deleteAgent(c *gin.Context) {
	ag, enrolled, ok := a.agentFor(c)
	if !ok {
		return
	}
	if !enrolled || !ag.Revoked {
		httperr.Abort(c, http.StatusConflict, "agent_not_revoked", "only a revoked agent can be deleted: revoke "+ag.ID+" first")
		return
	}
	if err := a.svc.Store().DeleteAgent(c.Request.Context(), ag.ID); err != nil {
		a.internal(c, err)
		return
	}
	a.audit(c, "agent.delete", ag.ID, "")
	c.JSON(http.StatusOK, gin.H{"deleted": ag.ID})
}

// listAudit is GET /audit?limit=: the latest write actions.
func (a *API) listAudit(c *gin.Context) {
	limit := defaultAuditLimit
	if l := c.Query("limit"); l != "" {
		n, err := strconv.Atoi(l)
		if err != nil || n < 1 || n > maxAuditLimit {
			httperr.Abort(c, http.StatusBadRequest, "invalid_limit", fmt.Sprintf("limit must be between 1 and %d", maxAuditLimit))
			return
		}
		limit = n
	}
	entries, err := a.svc.Store().AuditEntries(c.Request.Context(), limit)
	if err != nil {
		a.internal(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"entries": nonNil(entries)})
}
