package agenthub

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wolfx56/lum/deploy"
	"github.com/Wolfx56/lum/internal/agentproto"
	"github.com/Wolfx56/lum/internal/config"
)

// Self-update tuning.
const (
	selfUpdateTimeout = 5 * time.Minute  // download + replacement on the agent
	selfUpdateRetry   = 10 * time.Minute // between two orders to the same agent
)

var archPattern = regexp.MustCompile(`^[a-z0-9]+$`)

// binaries indexes the agent binaries of AgentDir (lum-agent-linux-<arch>).
type binaries struct {
	dir   string
	mu    sync.Mutex
	cache map[string]binary
}

type binary struct {
	path   string
	sha256 string
	size   int64
	mod    time.Time
}

// get returns the binary for arch; the checksum is recomputed only when the file changed.
func (b *binaries) get(arch string) (binary, bool) {
	if b.dir == "" || !archPattern.MatchString(arch) {
		return binary{}, false
	}
	path := filepath.Join(b.dir, agentproto.BinaryName(arch))
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return binary{}, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if c, ok := b.cache[arch]; ok && c.size == info.Size() && c.mod.Equal(info.ModTime()) {
		return c, true
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return binary{}, false
	}
	bin := binary{path: path, sha256: agentproto.Checksum(data), size: int64(len(data)), mod: info.ModTime()}
	if b.cache == nil {
		b.cache = map[string]binary{}
	}
	b.cache[arch] = bin
	return bin, true
}

// unavailable explains, for the logs, why get(arch) found no binary: most
// often a missing file or a directory that the server's user cannot traverse.
func (b *binaries) unavailable(arch string) string {
	if b.dir == "" {
		return "no agent directory (--agent-dir)"
	}
	if !archPattern.MatchString(arch) {
		return "invalid architecture"
	}
	f, err := os.Open(filepath.Join(b.dir, agentproto.BinaryName(arch)))
	if err != nil {
		return err.Error()
	}
	_ = f.Close()
	return "not a regular file"
}

// Download serves the agent binaries (DownloadPath + lum-agent-linux-<arch>),
// with their SHA-256 in X-Checksum-Sha256. Authentication (spec §10, no
// anonymous download): an enrolled agent (X-Agent-ID + Bearer token) or the
// bootstrap secret (Bearer, open enrollment only) for install-agent.sh.
func (h *Hub) Download(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, agentproto.DownloadPath)
	arch, ok := strings.CutPrefix(name, agentproto.BinaryName(""))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := h.authorizeDownload(r); err != nil {
		h.opts.Logger.Warn("agent binary download refused", "remote", r.RemoteAddr, "error", err.Error())
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	bin, ok := h.binaries.get(arch)
	if !ok {
		h.opts.Logger.Warn("agent binary unavailable", "arch", arch, "error", h.binaries.unavailable(arch))
		http.Error(w, "no agent binary for "+arch, http.StatusNotFound)
		return
	}
	f, err := os.Open(bin.path)
	if err != nil {
		http.Error(w, "agent binary unreadable", http.StatusInternalServerError)
		return
	}
	defer f.Close()
	w.Header().Set("X-Checksum-Sha256", bin.sha256)
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, agentproto.BinaryName(arch), bin.mod, f)
}

func (h *Hub) authorizeDownload(r *http.Request) error {
	bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || bearer == "" {
		return errors.New("missing bearer")
	}
	if id := r.Header.Get("X-Agent-ID"); id != "" {
		a, err := h.opts.Store.Agent(r.Context(), id)
		if err != nil || a.Revoked || subtle.ConstantTimeCompare([]byte(a.TokenHash), []byte(agentproto.Checksum([]byte(bearer)))) != 1 {
			return errInvalidToken
		}
		return nil
	}
	cfg := h.opts.Config()
	secret := cfg.Agents.SharedSecret.Reveal()
	if cfg.Agents.Enrollment != config.EnrollmentOpen || secret == "" ||
		subtle.ConstantTimeCompare([]byte(secret), []byte(bearer)) != 1 {
		return errEnrollment
	}
	return nil
}

var safeURL = regexp.MustCompile(`^https?://[A-Za-z0-9.:\[\]_-]+(/[A-Za-z0-9._~/-]*)?$`)

// InstallScript serves install-agent.sh with the server URL filled in: baseURL
// (server.base_url) or, if empty, the URL the request came through.
func (h *Hub) InstallScript(baseURL string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		server := strings.TrimRight(baseURL, "/")
		if server == "" {
			scheme := "http"
			if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
				scheme = "https"
			}
			server = (&url.URL{Scheme: scheme, Host: r.Host}).String()
		}
		if !safeURL.MatchString(server) {
			http.Error(w, "cannot build the server URL", http.StatusInternalServerError)
			return
		}
		script := strings.NewReplacer(
			"__LUM_SERVER_URL__", server,
			"__LUM_AGENT_SERVICE__", strings.TrimRight(deploy.AgentService, "\n"),
		).Replace(deploy.InstallAgent)
		w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
		w.Header().Set("Content-Length", strconv.Itoa(len(script)))
		_, _ = w.Write([]byte(script))
	}
}

// maybeSelfUpdate orders a self_update when the agent's executable differs
// from the binary of its architecture in AgentDir (agents.self_update: auto),
// unless a job is running on the agent.
func (h *Hub) maybeSelfUpdate(ctx context.Context, c *conn) {
	if h.opts.Config().Agents.SelfUpdate != config.SelfUpdateAuto {
		return
	}
	c.mu.Lock()
	running := c.binary
	c.mu.Unlock()
	bin, ok := h.binaries.get(running.Arch)
	if !ok || running.SHA256 == "" || running.SHA256 == bin.sha256 || h.busy(c) {
		return
	}
	h.selfUpdateMu.Lock()
	if last, ok := h.lastSelfUpd[c.id]; ok && h.opts.Now().Sub(last) < selfUpdateRetry {
		h.selfUpdateMu.Unlock()
		return
	}
	h.lastSelfUpd[c.id] = h.opts.Now()
	h.selfUpdateMu.Unlock()

	c.logger.Info("agent binary differs from the server's: self_update", "arch", running.Arch, "from", running.SHA256, "to", bin.sha256)
	if err := c.requestWithin(ctx, selfUpdateTimeout, agentproto.TypeSelfUpdate, agentproto.SelfUpdate{SHA256: bin.sha256, Size: bin.size}, nil); err != nil {
		c.logger.Error("self_update failed", "error", err.Error())
	}
}
