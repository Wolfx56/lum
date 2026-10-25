package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"

	"github.com/Wolfx56/lum/internal/agentproto"
	"github.com/Wolfx56/lum/internal/version"
)

// maxBinarySize bounds the downloaded agent binary.
const maxBinarySize = 200 << 20

// binary describes the running executable.
func (a *Agent) binary() agentproto.Binary {
	b := agentproto.Binary{Arch: runtime.GOARCH}
	if path, err := a.executable(); err == nil {
		if data, err := os.ReadFile(path); err == nil {
			b.SHA256 = agentproto.Checksum(data)
		}
	}
	return b
}

func (a *Agent) executable() (string, error) {
	if a.opts.Executable != "" {
		return a.opts.Executable, nil
	}
	path, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(path)
}

// selfUpdate downloads the new binary from the server, checks it and replaces
// the executable. The caller then stops the agent: systemd restarts it.
func (a *Agent) selfUpdate(ctx context.Context, req agentproto.SelfUpdate) error {
	if jobs := a.runningJobs(); len(jobs) > 0 {
		return fmt.Errorf("update scripts are running (%v): self_update refused", jobs)
	}
	path, err := a.executable()
	if err != nil {
		return err
	}
	token, err := a.readToken()
	if err != nil {
		return err
	}
	u, _ := url.Parse(a.wsURL)
	u.Scheme = map[string]string{"wss": "https", "ws": "http"}[u.Scheme]
	u.Path = u.Path[:len(u.Path)-len(agentproto.Path)] + agentproto.DownloadPath + agentproto.BinaryName(runtime.GOARCH)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("X-Agent-ID", a.opts.ID)
	httpReq.Header.Set("User-Agent", "lum-agent/"+version.Version)
	client := a.opts.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("downloading the agent binary: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading the agent binary: status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBinarySize+1))
	if err != nil {
		return fmt.Errorf("downloading the agent binary: %w", err)
	}
	if len(data) > maxBinarySize {
		return errors.New("agent binary too large")
	}
	if got := agentproto.Checksum(data); got != req.SHA256 || int64(len(data)) != req.Size {
		return fmt.Errorf("downloaded binary does not match the order (sha256 %s, %d bytes)", got, len(data))
	}
	if err := writeFileAtomic(path, data, 0o755); err != nil {
		return fmt.Errorf("replacing %s: %w", path, err)
	}
	a.logger.Info("agent binary replaced, restarting", "sha256", req.SHA256)
	return nil
}
