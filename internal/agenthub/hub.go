// Package agenthub is the server side of the agent protocol (spec §8): the
// WebSocket endpoint, enrollment, heartbeats, offline detection, orders sent to
// agents (get_version, run_update, cancel_job, self_update), targeted script
// synchronization (sync_scripts), agent binaries and install script.
// See doc/agent.md.
package agenthub

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/Wolfx56/lum/internal/agentproto"
	"github.com/Wolfx56/lum/internal/config"
	"github.com/Wolfx56/lum/internal/current"
	"github.com/Wolfx56/lum/internal/notify"
	"github.com/Wolfx56/lum/internal/scripts"
	"github.com/Wolfx56/lum/internal/store"
)

// Timeouts of the protocol.
const (
	HelloTimeout   = 10 * time.Second
	RequestTimeout = 30 * time.Second
	readLimit      = 4 << 20
)

var agentIDPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Hello refusals, sent in welcome.error.
var (
	errInvalidHello  = errors.New("invalid hello")
	errInvalidToken  = errors.New("invalid or revoked token")
	errEnrollment    = errors.New("enrollment refused")
	errSecretMissing = errors.New("enrollment not possible: agents.shared_secret is not configured")
)

// Options configures the Hub.
type Options struct {
	Config  func() *config.Config
	Store   *store.Store
	Scripts *scripts.Index
	// Notify sends agent_offline events; may be nil.
	Notify func(ctx context.Context, e notify.Event)
	Logger *slog.Logger
	Now    func() time.Time
	// AgentDir holds the agent binaries served for self_update and
	// install-agent.sh (lum-agent-linux-<arch>); empty = none.
	AgentDir string
}

// Hub tracks the connected agents. It is safe for concurrent use.
type Hub struct {
	opts Options

	mu    sync.Mutex
	conns map[string]*conn
	sinks map[string]*sink // running update scripts, by job id

	binaries *binaries

	selfUpdateMu sync.Mutex
	lastSelfUpd  map[string]time.Time // agent id → last self_update order
}

// New returns a Hub.
func New(opts Options) *Hub {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Hub{opts: opts, conns: map[string]*conn{}, sinks: map[string]*sink{},
		binaries: &binaries{dir: opts.AgentDir}, lastSelfUpd: map[string]time.Time{}}
}

// conn is one connected agent.
type conn struct {
	id     string
	ws     *websocket.Conn
	logger *slog.Logger
	cancel context.CancelFunc

	nextID  atomic.Uint64
	mu      sync.Mutex
	pending map[string]chan agentproto.Envelope
	scripts map[string]string // last reported by the agent: name → SHA-256
	saved   bool              // scripts already written to agent_scripts
	syncMu  sync.Mutex        // one sync_scripts at a time
	binary  agentproto.Binary // running executable, from hello/heartbeat
	running []string          // scripts running on the agent, from heartbeat
}

// Connected reports whether the agent has a live connection.
func (h *Hub) Connected(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.conns[id]
	return ok
}

// ConnectedCount returns the number of connected agents.
func (h *Hub) ConnectedCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.conns)
}

// ServeHTTP is the WebSocket endpoint (agentproto.Path). It is not behind the
// user authentication: the agent authenticates with its hello message.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return // Accept already wrote the HTTP error
	}
	ws.SetReadLimit(readLimit)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	hello, err := h.readHello(ctx, ws)
	if err != nil {
		h.opts.Logger.Warn("agent refused", "remote", r.RemoteAddr, "error", err.Error())
		h.refuse(ctx, ws, err)
		return
	}
	logger := h.opts.Logger.With("agent_id", hello.AgentID)
	token, err := h.authenticate(ctx, hello)
	if err != nil {
		logger.Warn("agent refused", "remote", r.RemoteAddr, "error", err.Error())
		h.refuse(ctx, ws, err)
		return
	}

	cfg := h.opts.Config()
	welcome, _ := agentproto.New(agentproto.TypeWelcome, "", agentproto.Welcome{
		Token:                    token,
		HeartbeatIntervalSeconds: int(cfg.Agents.HeartbeatInterval / time.Second),
	})
	if err := write(ctx, ws, welcome); err != nil {
		return
	}

	c := &conn{id: hello.AgentID, ws: ws, logger: logger, cancel: cancel, pending: map[string]chan agentproto.Envelope{}, binary: hello.Binary}
	h.register(c)
	defer h.unregister(c)
	logger.Info("agent connected", "remote", r.RemoteAddr, "agent_version", hello.AgentVersion, "hostname", hello.Hostname, "enrolled", token != "")
	h.seen(ctx, c, hello.AgentVersion, hello.Hostname, hello.Scripts)
	go h.syncIfNeeded(ctx, c)
	go h.maybeSelfUpdate(ctx, c)

	err = h.readLoop(ctx, c)
	logger.Info("agent disconnected", "reason", closeReason(err))
	_ = ws.Close(websocket.StatusNormalClosure, "")
}

func (h *Hub) readHello(ctx context.Context, ws *websocket.Conn) (agentproto.Hello, error) {
	ctx, cancel := context.WithTimeout(ctx, HelloTimeout)
	defer cancel()
	env, err := read(ctx, ws)
	if err != nil {
		return agentproto.Hello{}, fmt.Errorf("%w: %v", errInvalidHello, err)
	}
	var hello agentproto.Hello
	if env.Type != agentproto.TypeHello || env.Decode(&hello) != nil {
		return agentproto.Hello{}, fmt.Errorf("%w: expected a hello message", errInvalidHello)
	}
	if !agentIDPattern.MatchString(hello.AgentID) {
		return agentproto.Hello{}, fmt.Errorf("%w: agent_id must be kebab-case", errInvalidHello)
	}
	if (hello.Token == "") == (hello.Secret == "") {
		return agentproto.Hello{}, fmt.Errorf("%w: exactly one of token and secret is required", errInvalidHello)
	}
	return hello, nil
}

// authenticate checks the token, or enrolls the agent with the bootstrap secret
// and returns its new token.
func (h *Hub) authenticate(ctx context.Context, hello agentproto.Hello) (string, error) {
	if hello.Token != "" {
		a, err := h.opts.Store.Agent(ctx, hello.AgentID)
		if errors.Is(err, store.ErrNotFound) {
			return "", errInvalidToken
		}
		if err != nil {
			return "", err
		}
		if a.Revoked || subtle.ConstantTimeCompare([]byte(a.TokenHash), []byte(agentproto.Checksum([]byte(hello.Token)))) != 1 {
			return "", errInvalidToken
		}
		return "", nil
	}

	cfg := h.opts.Config()
	if cfg.Agents.Enrollment != config.EnrollmentOpen {
		return "", fmt.Errorf("%w: agents.enrollment is closed", errEnrollment)
	}
	secret := cfg.Agents.SharedSecret.Reveal()
	if secret == "" {
		return "", errSecretMissing
	}
	if subtle.ConstantTimeCompare([]byte(secret), []byte(hello.Secret)) != 1 {
		return "", fmt.Errorf("%w: wrong bootstrap secret", errEnrollment)
	}
	token, err := newToken()
	if err != nil {
		return "", err
	}
	err = h.opts.Store.EnrollAgent(ctx, hello.AgentID, agentproto.Checksum([]byte(token)), h.opts.Now())
	if errors.Is(err, store.ErrNotFound) {
		return "", fmt.Errorf("%w: agent is revoked", errEnrollment)
	}
	if err != nil {
		return "", err
	}
	h.opts.Logger.Info("agent enrolled", "agent_id", hello.AgentID)
	return token, nil
}

func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (h *Hub) refuse(ctx context.Context, ws *websocket.Conn, reason error) {
	msg := reason.Error()
	// Internal errors are not detailed to the agent.
	if !errors.Is(reason, errInvalidHello) && !errors.Is(reason, errInvalidToken) &&
		!errors.Is(reason, errEnrollment) && !errors.Is(reason, errSecretMissing) {
		msg = "internal error"
	}
	env := agentproto.Envelope{Type: agentproto.TypeWelcome, Error: msg}
	wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_ = write(wctx, ws, env)
	_ = ws.Close(websocket.StatusPolicyViolation, "refused")
}

// register makes c the agent's connection, closing a previous one.
func (h *Hub) register(c *conn) {
	h.mu.Lock()
	old := h.conns[c.id]
	h.conns[c.id] = c
	h.mu.Unlock()
	if old != nil {
		old.logger.Info("agent connection replaced by a new one")
		_ = old.ws.Close(websocket.StatusPolicyViolation, "replaced by a new connection")
		old.cancel()
	}
}

func (h *Hub) unregister(c *conn) {
	h.mu.Lock()
	if h.conns[c.id] == c {
		delete(h.conns, c.id)
	}
	h.mu.Unlock()
	c.mu.Lock()
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	c.mu.Unlock()
}

func (h *Hub) readLoop(ctx context.Context, c *conn) error {
	for {
		env, err := read(ctx, c.ws)
		if err != nil {
			return err
		}
		switch env.Type {
		case agentproto.TypeHeartbeat:
			var hb agentproto.Heartbeat
			if err := env.Decode(&hb); err != nil {
				c.logger.Warn("invalid heartbeat", "error", err.Error())
				continue
			}
			c.mu.Lock()
			c.binary, c.running = hb.Binary, hb.RunningJobs
			c.mu.Unlock()
			h.seen(ctx, c, hb.AgentVersion, hb.Hostname, hb.Scripts)
			go h.syncIfNeeded(ctx, c)
			go h.maybeSelfUpdate(ctx, c)
		case agentproto.TypeJobLog, agentproto.TypeJobDone:
			h.jobEvent(c, env)
		case agentproto.TypeResponse:
			c.mu.Lock()
			ch, ok := c.pending[env.ID]
			delete(c.pending, env.ID)
			c.mu.Unlock()
			if ok {
				ch <- env
			}
		default:
			c.logger.Warn("unexpected message from agent", "type", env.Type)
		}
	}
}

// seen records a hello or heartbeat: agent online, reported scripts.
func (h *Hub) seen(ctx context.Context, c *conn, agentVersion, hostname string, reported map[string]string) {
	now := h.opts.Now()
	if err := h.opts.Store.AgentSeen(ctx, c.id, agentVersion, hostname, now); err != nil {
		c.logger.Error("agent state not saved", "error", err.Error())
	}
	c.mu.Lock()
	changed := !c.saved || !maps.Equal(c.scripts, reported)
	c.scripts, c.saved = reported, true
	c.mu.Unlock()
	if changed {
		h.saveScripts(ctx, c, reported)
	}
}

func (h *Hub) saveScripts(ctx context.Context, c *conn, reported map[string]string) {
	if err := h.opts.Store.SetAgentScripts(ctx, c.id, reported, h.opts.Now()); err != nil {
		c.logger.Error("agent scripts not saved", "error", err.Error())
	}
}

// request sends an order and waits for the response (or ctx, or RequestTimeout).
func (c *conn) request(ctx context.Context, typ string, payload, out any) error {
	return c.requestWithin(ctx, RequestTimeout, typ, payload, out)
}

func (c *conn) requestWithin(ctx context.Context, timeout time.Duration, typ string, payload, out any) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	id := strconv.FormatUint(c.nextID.Add(1), 10)
	env, err := agentproto.New(typ, id, payload)
	if err != nil {
		return err
	}
	ch := make(chan agentproto.Envelope, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	if err := write(ctx, c.ws, env); err != nil {
		return fmt.Errorf("sending %s: %w", typ, err)
	}
	select {
	case resp, ok := <-ch:
		if !ok {
			return fmt.Errorf("%s: agent disconnected", typ)
		}
		if resp.Error != "" {
			return fmt.Errorf("%s: agent error: %s", typ, resp.Error)
		}
		if out != nil {
			return resp.Decode(out)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("%s: no response from agent: %w", typ, ctx.Err())
	}
}

func (h *Hub) conn(id string) (*conn, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok := h.conns[id]
	if !ok {
		return nil, fmt.Errorf("%w (%s)", current.ErrAgentUnavailable, id)
	}
	return c, nil
}

// RunCommand implements current.AgentRunner: get_version on the agent.
func (h *Hub) RunCommand(ctx context.Context, agentID string, command []string) (agentproto.GetVersionResult, error) {
	c, err := h.conn(agentID)
	if err != nil {
		return agentproto.GetVersionResult{}, err
	}
	var res agentproto.GetVersionResult
	err = c.request(ctx, agentproto.TypeGetVersion, agentproto.GetVersion{Command: command}, &res)
	return res, err
}

// Desired returns the scripts an agent must hold: those referenced by the
// update.type script recipes of its apps (targeted sync, spec §9).
func (h *Hub) Desired(agentID string) map[string]scripts.Script {
	out := map[string]scripts.Script{}
	for _, app := range h.opts.Config().Apps {
		if app.Agent != agentID || app.Update == nil || app.Update.Type != config.TypeScript {
			continue
		}
		if s, ok := h.opts.Scripts.Get(app.Update.Script); ok {
			out[s.Name] = s
		} else {
			h.opts.Logger.Warn("script referenced but absent from scripts.dir", "agent_id", agentID, "app_id", app.ID, "script", app.Update.Script)
		}
	}
	return out
}

// SyncAll pushes scripts to every connected agent whose copy differs (after a
// script change or a configuration reload).
func (h *Hub) SyncAll(ctx context.Context) {
	h.mu.Lock()
	conns := slices.Collect(maps.Values(h.conns))
	h.mu.Unlock()
	for _, c := range conns {
		go h.syncIfNeeded(ctx, c)
	}
}

// syncIfNeeded sends sync_scripts if the scripts reported by the agent differ
// from the desired set. It does nothing if a sync is already in progress.
func (h *Hub) syncIfNeeded(ctx context.Context, c *conn) {
	if !c.syncMu.TryLock() {
		return
	}
	defer c.syncMu.Unlock()
	if err := h.syncLocked(ctx, c, false); err != nil {
		c.logger.Error("scripts sync failed", "error", err.Error())
	}
}

// syncLocked synchronizes the agent's scripts (even when they look in sync if
// force); c.syncMu must be held.
func (h *Hub) syncLocked(ctx context.Context, c *conn, force bool) error {
	desired := h.Desired(c.id)
	want := make(map[string]string, len(desired))
	for name, s := range desired {
		want[name] = s.SHA256
	}
	c.mu.Lock()
	inSync := maps.Equal(c.scripts, want)
	c.mu.Unlock()
	if inSync && !force {
		return nil
	}

	msg := agentproto.SyncScripts{Scripts: []agentproto.Script{}}
	for _, name := range slices.Sorted(maps.Keys(desired)) {
		s := desired[name]
		msg.Scripts = append(msg.Scripts, agentproto.Script{Name: s.Name, Content: s.Content, SHA256: s.SHA256, Mode: uint32(s.Mode)})
	}
	var res agentproto.SyncResult
	if err := c.request(ctx, agentproto.TypeSyncScripts, msg, &res); err != nil {
		return err
	}
	c.mu.Lock()
	c.scripts = res.Scripts
	c.mu.Unlock()
	h.saveScripts(ctx, c, res.Scripts)
	c.logger.Info("scripts synced", "scripts", slices.Sorted(maps.Keys(res.Scripts)))
	return nil
}

// CloseAll closes every agent connection (server shutdown); agents reconnect.
func (h *Hub) CloseAll() {
	h.mu.Lock()
	conns := slices.Collect(maps.Values(h.conns))
	h.mu.Unlock()
	for _, c := range conns {
		_ = c.ws.Close(websocket.StatusGoingAway, "server shutting down")
		c.cancel()
	}
}

// Run marks offline the agents without heartbeat for agents.offline_after and
// notifies agent_offline, until ctx is cancelled.
func (h *Hub) Run(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		h.Sweep(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Sweep runs one offline detection pass.
func (h *Hub) Sweep(ctx context.Context) {
	cfg := h.opts.Config()
	ids, err := h.opts.Store.MarkAgentsOffline(ctx, h.opts.Now().Add(-cfg.Agents.OfflineAfter))
	if err != nil {
		h.opts.Logger.Error("offline detection failed", "error", err.Error())
		return
	}
	for _, id := range ids {
		h.opts.Logger.Warn("agent offline", "agent_id", id, "offline_after", cfg.Agents.OfflineAfter.String())
		h.mu.Lock()
		c := h.conns[id]
		h.mu.Unlock()
		if c != nil {
			_ = c.ws.Close(websocket.StatusGoingAway, "no heartbeat")
			c.cancel()
		}
		if h.opts.Notify != nil {
			h.opts.Notify(ctx, notify.Event{
				Type:    config.EventAgentOffline,
				Title:   "Agent " + id + " offline",
				Message: "No heartbeat for " + cfg.Agents.OfflineAfter.String(),
			})
		}
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

func closeReason(err error) string {
	if s := websocket.CloseStatus(err); s != -1 {
		return s.String()
	}
	if errors.Is(err, context.Canceled) {
		return "closed by server"
	}
	return err.Error()
}
