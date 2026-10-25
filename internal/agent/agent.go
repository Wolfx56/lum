// Package agent implements lum-agent (spec §8): an outbound WebSocket to the
// server, enrollment, heartbeats and the orders get_version, sync_scripts,
// run_update, cancel_job and self_update.
// See doc/agent.md.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/Wolfx56/lum/internal/agentproto"
	"github.com/Wolfx56/lum/internal/version"
)

// Defaults of the reconnection backoff (spec §13: 1 s → 60 s).
const (
	DefaultMinBackoff = time.Second
	DefaultMaxBackoff = 60 * time.Second
)

const (
	helloTimeout       = 10 * time.Second
	defaultHeartbeat   = 60 * time.Second
	commandTimeout     = 25 * time.Second // below the server request timeout (30 s)
	maxCommandOutput   = 64 << 10
	readLimit          = 16 << 20 // sync_scripts carries whole scripts
	tokenFilePerm      = 0o600
	stateDirPerm       = 0o700
	scriptsDirPerm     = 0o700
	scriptsMinimumPerm = 0o700
)

// Options configures the agent.
type Options struct {
	ID        string
	ServerURL string // https://lum.example.com (http:// only towards localhost)
	// Secret is the bootstrap secret, used only while TokenFile does not exist.
	Secret     string
	TokenFile  string // e.g. /opt/lum-agent/token
	ScriptsDir string // e.g. /opt/lum-agent/scripts

	HTTPClient *http.Client // optional (tests); also downloads the self_update binary
	// Executable is the file replaced by self_update; empty = os.Executable (tests).
	Executable string
	MinBackoff time.Duration
	MaxBackoff time.Duration
	Logger     *slog.Logger
}

// Agent is a running lum-agent.
type Agent struct {
	opts    Options
	wsURL   string
	logger  *slog.Logger
	started time.Time

	syncMu sync.Mutex // one sync_scripts at a time

	jobsMu sync.Mutex
	jobs   map[string]*runningJob
	outbox *outbox

	restart context.CancelFunc // stops Run after a self_update
}

// errRefused is a refusal by the server (bad token, closed enrollment...).
var errRefused = errors.New("refused by the server")

// New validates the options.
func New(opts Options) (*Agent, error) {
	if opts.ID == "" {
		return nil, errors.New("agent id is required")
	}
	if opts.TokenFile == "" || opts.ScriptsDir == "" {
		return nil, errors.New("token file and scripts directory are required")
	}
	wsURL, err := websocketURL(opts.ServerURL)
	if err != nil {
		return nil, err
	}
	// Scripts run in "/": a relative scripts directory would no longer resolve.
	if opts.ScriptsDir, err = filepath.Abs(opts.ScriptsDir); err != nil {
		return nil, err
	}
	if opts.MinBackoff <= 0 {
		opts.MinBackoff = DefaultMinBackoff
	}
	if opts.MaxBackoff < opts.MinBackoff {
		opts.MaxBackoff = max(DefaultMaxBackoff, opts.MinBackoff)
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	return &Agent{
		opts: opts, wsURL: wsURL, logger: opts.Logger.With("agent_id", opts.ID), started: time.Now(),
		jobs: map[string]*runningJob{}, outbox: newOutbox(),
	}, nil
}

// websocketURL turns the server base URL into the WebSocket URL. TLS is
// mandatory (spec §10) except towards a loopback address (development).
func websocketURL(server string) (string, error) {
	u, err := url.Parse(server)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("invalid server URL %q", server)
	}
	switch u.Scheme {
	case "https", "wss":
		u.Scheme = "wss"
	case "http", "ws":
		if !isLoopback(u.Hostname()) {
			return "", fmt.Errorf("server URL %q: TLS is required (https://), plain http is only allowed towards localhost", server)
		}
		u.Scheme = "ws"
	default:
		return "", fmt.Errorf("server URL %q: scheme must be https", server)
	}
	u.Path = strings.TrimRight(u.Path, "/") + agentproto.Path
	return u.String(), nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Run connects to the server and serves its orders until ctx is cancelled,
// reconnecting with exponential backoff. After a self_update it returns nil:
// the service manager (systemd, Restart=always) starts the new binary.
func (a *Agent) Run(ctx context.Context) error {
	if _, err := a.readToken(); err != nil && a.opts.Secret == "" {
		return fmt.Errorf("no token (%s) and no bootstrap secret: cannot enroll", a.opts.TokenFile)
	}
	ctx, a.restart = context.WithCancel(ctx)
	defer a.restart()
	a.logger.Info("agent started", "server", a.wsURL)
	backoff := a.opts.MinBackoff
	for {
		welcomed, err := a.session(ctx)
		if ctx.Err() != nil {
			a.logger.Info("agent stopped")
			return nil
		}
		if welcomed {
			backoff = a.opts.MinBackoff
		}
		level := slog.LevelWarn
		if errors.Is(err, errRefused) {
			level = slog.LevelError
		}
		a.logger.Log(ctx, level, "disconnected from server", "error", err.Error(), "retry_in", backoff.String())
		// ±20 % jitter so that agents do not reconnect all at once after a server restart.
		wait := time.Duration(float64(backoff) * (0.8 + 0.4*rand.Float64()))
		select {
		case <-ctx.Done():
			a.logger.Info("agent stopped")
			return nil
		case <-time.After(wait):
		}
		backoff = min(backoff*2, a.opts.MaxBackoff)
	}
}

// session runs one connection; welcomed reports whether the server accepted the agent.
func (a *Agent) session(ctx context.Context) (welcomed bool, err error) {
	dialCtx, cancel := context.WithTimeout(ctx, helloTimeout)
	defer cancel()
	ws, _, err := websocket.Dial(dialCtx, a.wsURL, &websocket.DialOptions{
		HTTPClient: a.opts.HTTPClient,
		HTTPHeader: http.Header{"User-Agent": {"lum-agent/" + version.Version}},
	})
	if err != nil {
		return false, err
	}
	defer func() { _ = ws.CloseNow() }()
	ws.SetReadLimit(readLimit)

	interval, err := a.hello(dialCtx, ws)
	if err != nil {
		return false, err
	}
	a.logger.Info("connected to server", "heartbeat_interval", interval.String())

	sessCtx, stop := context.WithCancel(ctx)
	defer stop()
	go a.heartbeats(sessCtx, ws, interval)
	// Job events queued while disconnected are sent first, then as they come.
	go a.flush(sessCtx, func(env agentproto.Envelope) error { return write(sessCtx, ws, env) })

	for {
		env, err := read(sessCtx, ws)
		if err != nil {
			return true, err
		}
		switch env.Type {
		case agentproto.TypeGetVersion, agentproto.TypeSyncScripts, agentproto.TypeRunUpdate,
			agentproto.TypeCancelJob, agentproto.TypeSelfUpdate:
			go a.handle(sessCtx, ws, env)
		default:
			a.logger.Warn("unexpected message from server", "type", env.Type)
		}
	}
}

// hello authenticates and returns the heartbeat interval.
func (a *Agent) hello(ctx context.Context, ws *websocket.Conn) (time.Duration, error) {
	token, _ := a.readToken()
	hostname, _ := os.Hostname()
	h := agentproto.Hello{AgentID: a.opts.ID, AgentVersion: version.Version, Hostname: hostname, Scripts: a.localScripts(), Binary: a.binary()}
	if token != "" {
		h.Token = token
	} else {
		h.Secret = a.opts.Secret
	}
	env, err := agentproto.New(agentproto.TypeHello, "", h)
	if err != nil {
		return 0, err
	}
	if err := write(ctx, ws, env); err != nil {
		return 0, err
	}
	resp, err := read(ctx, ws)
	if err != nil {
		return 0, fmt.Errorf("waiting for welcome: %w", err)
	}
	if resp.Type != agentproto.TypeWelcome {
		return 0, fmt.Errorf("unexpected %q instead of welcome", resp.Type)
	}
	if resp.Error != "" {
		return 0, fmt.Errorf("%w: %s", errRefused, resp.Error)
	}
	var w agentproto.Welcome
	if err := resp.Decode(&w); err != nil {
		return 0, err
	}
	if w.Token != "" {
		if err := a.saveToken(w.Token); err != nil {
			return 0, fmt.Errorf("enrolled but the token could not be saved: %w", err)
		}
		a.logger.Info("agent enrolled, token saved", "file", a.opts.TokenFile)
	}
	interval := time.Duration(w.HeartbeatIntervalSeconds) * time.Second
	if interval <= 0 {
		interval = defaultHeartbeat
	}
	return interval, nil
}

func (a *Agent) readToken() (string, error) {
	data, err := os.ReadFile(a.opts.TokenFile)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", errors.New("empty token file")
	}
	return token, nil
}

// saveToken writes the token atomically, readable by root only.
func (a *Agent) saveToken(token string) error {
	if err := os.MkdirAll(filepath.Dir(a.opts.TokenFile), stateDirPerm); err != nil {
		return err
	}
	return writeFileAtomic(a.opts.TokenFile, []byte(token+"\n"), tokenFilePerm)
}

func (a *Agent) heartbeats(ctx context.Context, ws *websocket.Conn, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		hostname, _ := os.Hostname()
		env, _ := agentproto.New(agentproto.TypeHeartbeat, "", agentproto.Heartbeat{
			AgentVersion:  version.Version,
			Hostname:      hostname,
			UptimeSeconds: a.uptime(),
			Scripts:       a.localScripts(),
			Binary:        a.binary(),
			RunningJobs:   a.runningJobs(),
		})
		if err := write(ctx, ws, env); err != nil {
			a.logger.Warn("heartbeat not sent", "error", err.Error())
			return
		}
	}
}

// uptime is the container uptime (/proc/uptime), or the agent's own.
func (a *Agent) uptime() int64 {
	if data, err := os.ReadFile("/proc/uptime"); err == nil {
		var secs float64
		if _, err := fmt.Sscanf(string(data), "%f", &secs); err == nil {
			return int64(secs)
		}
	}
	return int64(time.Since(a.started).Seconds())
}

// handle executes one order and sends the response.
func (a *Agent) handle(ctx context.Context, ws *websocket.Conn, env agentproto.Envelope) {
	var payload any
	var err error
	switch env.Type {
	case agentproto.TypeGetVersion:
		var req agentproto.GetVersion
		if err = env.Decode(&req); err == nil {
			payload, err = a.getVersion(ctx, req)
		}
	case agentproto.TypeSyncScripts:
		var req agentproto.SyncScripts
		if err = env.Decode(&req); err == nil {
			payload, err = a.syncScripts(req)
		}
	case agentproto.TypeRunUpdate:
		var req agentproto.RunUpdate
		if err = env.Decode(&req); err == nil {
			err = a.runUpdate(req)
		}
	case agentproto.TypeCancelJob:
		var req agentproto.CancelJob
		if err = env.Decode(&req); err == nil {
			err = a.cancelJob(req)
		}
	case agentproto.TypeSelfUpdate:
		var req agentproto.SelfUpdate
		if err = env.Decode(&req); err == nil {
			err = a.selfUpdate(ctx, req)
		}
	}
	resp, mErr := agentproto.New(agentproto.TypeResponse, env.ID, payload)
	if mErr != nil {
		err = mErr
	}
	if err != nil {
		a.logger.Warn("order failed", "order", env.Type, "error", err.Error())
		resp = agentproto.Envelope{Type: agentproto.TypeResponse, ID: env.ID, Error: err.Error()}
	}
	if err := write(ctx, ws, resp); err != nil {
		a.logger.Warn("response not sent", "order", env.Type, "error", err.Error())
	}
	if env.Type == agentproto.TypeSelfUpdate && err == nil {
		_ = ws.Close(websocket.StatusNormalClosure, "restarting after self_update")
		a.restart()
	}
}

func read(ctx context.Context, ws *websocket.Conn) (agentproto.Envelope, error) {
	_, data, err := ws.Read(ctx)
	if err != nil {
		return agentproto.Envelope{}, err
	}
	var env agentproto.Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return agentproto.Envelope{}, fmt.Errorf("invalid message: %w", err)
	}
	return env, nil
}

func write(ctx context.Context, ws *websocket.Conn, env agentproto.Envelope) error {
	data, err := json.Marshal(env)
	if err != nil {
		return err
	}
	return ws.Write(ctx, websocket.MessageText, data)
}
