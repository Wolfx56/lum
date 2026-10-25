// Package agentproto defines the messages exchanged between lum-server and
// lum-agent over the WebSocket (spec §8). Every message is a JSON Envelope.
// See doc/agent.md.
package agentproto

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// Path is the WebSocket endpoint on the server.
const Path = "/agent/ws"

// DownloadPath serves the agent binaries: DownloadPath + BinaryName(arch).
const DownloadPath = "/agent/download/"

// BinaryName is the file name of the agent binary for a Linux architecture.
func BinaryName(arch string) string { return "lum-agent-linux-" + arch }

// Message types.
const (
	// Agent → server.
	TypeHello     = "hello"     // first message: identification (token or bootstrap secret)
	TypeHeartbeat = "heartbeat" // every heartbeat_interval
	TypeResponse  = "response"  // answer to an order, same ID
	TypeJobLog    = "job_log"   // one output line of a running update script
	TypeJobDone   = "job_done"  // end of an update script

	// Server → agent.
	TypeWelcome     = "welcome" // answer to hello; Error set when refused
	TypeGetVersion  = "get_version"
	TypeSyncScripts = "sync_scripts"
	TypeRunUpdate   = "run_update"
	TypeCancelJob   = "cancel_job"
	TypeSelfUpdate  = "self_update"
)

// Envelope wraps every message. ID correlates an order with its response.
// Error is set on a response (or welcome) when the request failed.
type Envelope struct {
	Type    string          `json:"type"`
	ID      string          `json:"id,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
	Error   string          `json:"error,omitempty"`
}

// New builds an envelope; payload may be nil.
func New(typ, id string, payload any) (Envelope, error) {
	e := Envelope{Type: typ, ID: id}
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return Envelope{}, err
		}
		e.Payload = data
	}
	return e, nil
}

// Decode unmarshals the payload into v.
func (e Envelope) Decode(v any) error { return json.Unmarshal(e.Payload, v) }

// Hello identifies the agent. Exactly one of Token (enrolled agent) and Secret
// (bootstrap enrollment) is set.
type Hello struct {
	AgentID      string            `json:"agent_id"`
	Token        string            `json:"token,omitempty"`
	Secret       string            `json:"secret,omitempty"`
	AgentVersion string            `json:"agent_version"`
	Hostname     string            `json:"hostname"`
	Scripts      map[string]string `json:"scripts"` // local scripts: name → SHA-256
	Binary       Binary            `json:"binary"`
}

// Binary identifies the running agent executable (self_update decisions).
type Binary struct {
	Arch   string `json:"arch"`   // GOARCH: amd64, arm64…
	SHA256 string `json:"sha256"` // of the executable file, empty if unreadable
}

// Welcome accepts the agent. Token is only set on enrollment: the agent must
// persist it and use it from then on.
type Welcome struct {
	Token                    string `json:"token,omitempty"`
	HeartbeatIntervalSeconds int    `json:"heartbeat_interval_seconds"`
}

// Heartbeat reports the agent state.
type Heartbeat struct {
	AgentVersion  string            `json:"agent_version"`
	Hostname      string            `json:"hostname"`
	UptimeSeconds int64             `json:"uptime_seconds"`
	Scripts       map[string]string `json:"scripts"`
	Binary        Binary            `json:"binary"`
	RunningJobs   []string          `json:"running_jobs"`
}

// GetVersion asks the agent to run the recipe command (current.command).
type GetVersion struct {
	Command []string `json:"command"`
}

// GetVersionResult is the command output; the server applies output_regex.
type GetVersionResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

// Script is one update script sent to an agent.
type Script struct {
	Name    string `json:"name"`
	Content []byte `json:"content"` // base64 in JSON
	SHA256  string `json:"sha256"`
	Mode    uint32 `json:"mode"`
}

// SyncScripts is the complete set of scripts the agent must hold: scripts
// absent from the list are removed from the agent's scripts directory.
type SyncScripts struct {
	Scripts []Script `json:"scripts"`
}

// SyncResult lists the scripts present on the agent after the sync.
type SyncResult struct {
	Scripts map[string]string `json:"scripts"`
}

// RunUpdate asks the agent to run an update script. The agent acknowledges at
// once (response), then streams job_log messages and ends with job_done.
type RunUpdate struct {
	JobID  string            `json:"job_id"`
	Script string            `json:"script"`
	SHA256 string            `json:"sha256"` // the local copy must match, otherwise the order is refused
	Env    map[string]string `json:"env"`    // LUM_APP_ID, LUM_APP_NAME, LUM_CURRENT_VERSION, LUM_TARGET_VERSION, LUM_JOB_ID
}

// JobLog is one line written by the script.
type JobLog struct {
	JobID  string `json:"job_id"`
	Stream string `json:"stream"` // stdout | stderr
	Line   string `json:"line"`
}

// JobDone ends a script: exit code, or Error when it could not run or was killed.
type JobDone struct {
	JobID     string `json:"job_id"`
	ExitCode  int    `json:"exit_code"`
	Cancelled bool   `json:"cancelled"`
	Error     string `json:"error,omitempty"`
}

// CancelJob asks the agent to stop a script: SIGTERM, then SIGKILL after 30 s.
type CancelJob struct {
	JobID string `json:"job_id"`
}

// SelfUpdate asks the agent to replace its executable with the binary served
// at DownloadPath + BinaryName(arch), then to exit so that systemd restarts it.
type SelfUpdate struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Checksum returns the hex SHA-256 of data.
func Checksum(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
