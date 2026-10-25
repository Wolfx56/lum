package agenthub

import (
	"context"
	"errors"
	"fmt"

	"github.com/Wolfx56/lum/internal/agentproto"
)

// ErrScriptNotSynced is returned when the agent's copy of a script cannot be
// brought in line with scripts.dir.
var ErrScriptNotSynced = errors.New("script not synchronized on the agent")

// sink receives the events of one running update script. It does not depend
// on a connection: the agent keeps the events while disconnected and sends
// them after reconnecting.
type sink struct {
	agentID string
	onLog   func(stream, line string)
	done    chan agentproto.JobDone
}

// jobEvent routes job_log and job_done to the job's sink.
func (h *Hub) jobEvent(c *conn, env agentproto.Envelope) {
	switch env.Type {
	case agentproto.TypeJobLog:
		var l agentproto.JobLog
		if env.Decode(&l) != nil {
			return
		}
		if s := h.sink(l.JobID); s != nil && s.agentID == c.id {
			s.onLog(l.Stream, l.Line)
		}
	case agentproto.TypeJobDone:
		var d agentproto.JobDone
		if env.Decode(&d) != nil {
			return
		}
		s := h.sink(d.JobID)
		if s == nil || s.agentID != c.id {
			// Job unknown to this server process (restarted meanwhile): already failed.
			c.logger.Warn("end of an unknown job ignored", "job_id", d.JobID, "exit_code", d.ExitCode)
			return
		}
		select {
		case s.done <- d:
		default:
		}
	}
}

func (h *Hub) sink(jobID string) *sink {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sinks[jobID]
}

// EnsureScript makes sure the agent holds the current version of script
// (synchronizing it if needed) and returns its checksum (spec §9: checked
// before every run_update).
func (h *Hub) EnsureScript(ctx context.Context, agentID, script string) (string, error) {
	c, err := h.conn(agentID)
	if err != nil {
		return "", err
	}
	s, ok := h.opts.Scripts.Get(script)
	if !ok {
		return "", fmt.Errorf("%w: %s is absent from scripts.dir", ErrScriptNotSynced, script)
	}
	if _, ok := h.Desired(agentID)[script]; !ok {
		return "", fmt.Errorf("%w: %s is not referenced by a recipe of agent %s", ErrScriptNotSynced, script, agentID)
	}
	c.syncMu.Lock()
	defer c.syncMu.Unlock()
	if err := h.syncLocked(ctx, c, false); err != nil {
		return "", fmt.Errorf("%w: %v", ErrScriptNotSynced, err)
	}
	c.mu.Lock()
	got := c.scripts[script]
	c.mu.Unlock()
	if got != s.SHA256 {
		return "", fmt.Errorf("%w: %s (agent %s, server %s)", ErrScriptNotSynced, script, got, s.SHA256)
	}
	return s.SHA256, nil
}

// RunUpdate runs an update script on the agent and blocks until it ends (or
// ctx is done). Every output line is passed to onLog.
func (h *Hub) RunUpdate(ctx context.Context, agentID string, req agentproto.RunUpdate, onLog func(stream, line string)) (agentproto.JobDone, error) {
	c, err := h.conn(agentID)
	if err != nil {
		return agentproto.JobDone{}, err
	}
	s := &sink{agentID: agentID, onLog: onLog, done: make(chan agentproto.JobDone, 1)}
	h.mu.Lock()
	h.sinks[req.JobID] = s
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.sinks, req.JobID)
		h.mu.Unlock()
	}()

	if err := c.request(ctx, agentproto.TypeRunUpdate, req, nil); err != nil {
		return agentproto.JobDone{}, err
	}
	select {
	case d := <-s.done:
		return d, nil
	case <-ctx.Done():
		return agentproto.JobDone{}, ctx.Err()
	}
}

// CancelJob asks the agent to stop a running script (SIGTERM, SIGKILL after 30 s).
func (h *Hub) CancelJob(ctx context.Context, agentID, jobID string) error {
	c, err := h.conn(agentID)
	if err != nil {
		return err
	}
	return c.request(ctx, agentproto.TypeCancelJob, agentproto.CancelJob{JobID: jobID}, nil)
}

// busy reports whether a job runs on the agent (server or agent side).
func (h *Hub) busy(c *conn) bool {
	h.mu.Lock()
	for _, s := range h.sinks {
		if s.agentID == c.id {
			h.mu.Unlock()
			return true
		}
	}
	h.mu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.running) > 0
}
