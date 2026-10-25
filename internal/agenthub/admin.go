package agenthub

import (
	"context"
	"maps"

	"github.com/coder/websocket"
)

// Binary states of a connected agent (BinaryStatus).
const (
	BinaryUpToDate = "up_to_date" // same executable as the server's AgentDir
	BinaryOutdated = "outdated"   // differs: self_update pending (or disabled)
	BinaryUnknown  = "unknown"    // not connected, or no binary for its architecture
)

// SyncScripts forces a sync_scripts on the agent (button of the agents page,
// spec §9) and returns the scripts it now holds (name → SHA-256).
func (h *Hub) SyncScripts(ctx context.Context, agentID string) (map[string]string, error) {
	c, err := h.conn(agentID)
	if err != nil {
		return nil, err
	}
	c.syncMu.Lock()
	defer c.syncMu.Unlock()
	if err := h.syncLocked(ctx, c, true); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return maps.Clone(c.scripts), nil
}

// Disconnect closes the agent's connection, if any (revocation: the agent's
// next hello is refused).
func (h *Hub) Disconnect(agentID string) {
	h.mu.Lock()
	c := h.conns[agentID]
	h.mu.Unlock()
	if c != nil {
		c.logger.Info("agent disconnected by the server")
		_ = c.ws.Close(websocket.StatusPolicyViolation, "revoked")
		c.cancel()
	}
}

// BinaryStatus compares the executable of a connected agent with the binary
// the server distributes for its architecture.
func (h *Hub) BinaryStatus(agentID string) string {
	c, err := h.conn(agentID)
	if err != nil {
		return BinaryUnknown
	}
	c.mu.Lock()
	running := c.binary
	c.mu.Unlock()
	bin, ok := h.binaries.get(running.Arch)
	switch {
	case !ok || running.SHA256 == "":
		return BinaryUnknown
	case running.SHA256 == bin.sha256:
		return BinaryUpToDate
	default:
		return BinaryOutdated
	}
}
