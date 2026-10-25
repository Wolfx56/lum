package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Wolfx56/lum/internal/agentproto"
)

// getVersion runs the recipe command (no shell) and returns its output.
func (a *Agent) getVersion(ctx context.Context, req agentproto.GetVersion) (agentproto.GetVersionResult, error) {
	if len(req.Command) == 0 || req.Command[0] == "" {
		return agentproto.GetVersionResult{}, errors.New("empty command")
	}
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, req.Command[0], req.Command[1:]...)
	stdout := &limitedBuffer{max: maxCommandOutput}
	stderr := &limitedBuffer{max: maxCommandOutput}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	res := agentproto.GetVersionResult{Stdout: stdout.String(), Stderr: stderr.String()}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case ctx.Err() != nil:
		return res, fmt.Errorf("%s: timed out after %s", req.Command[0], commandTimeout)
	case errors.As(err, &exitErr):
		res.ExitCode = exitErr.ExitCode()
	default:
		return res, err // not found, not executable...
	}
	return res, nil
}

// limitedBuffer keeps the first max bytes written and drops the rest. The
// buffer is not embedded: its ReadFrom would bypass the limit.
type limitedBuffer struct {
	buf bytes.Buffer
	max int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.buf.Len(); room > 0 {
		b.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string { return b.buf.String() }

// syncScripts makes the scripts directory hold exactly the received scripts.
// Nothing is written if one script is invalid or its checksum does not match.
func (a *Agent) syncScripts(req agentproto.SyncScripts) (agentproto.SyncResult, error) {
	a.syncMu.Lock()
	defer a.syncMu.Unlock()

	keep := map[string]bool{}
	for _, s := range req.Scripts {
		if !validScriptName(s.Name) {
			return agentproto.SyncResult{}, fmt.Errorf("invalid script name %q", s.Name)
		}
		if got := agentproto.Checksum(s.Content); got != s.SHA256 {
			return agentproto.SyncResult{}, fmt.Errorf("%s: checksum mismatch (got %s, want %s)", s.Name, got, s.SHA256)
		}
		keep[s.Name] = true
	}
	if err := os.MkdirAll(a.opts.ScriptsDir, scriptsDirPerm); err != nil {
		return agentproto.SyncResult{}, err
	}

	local := a.localScripts()
	for _, s := range req.Scripts {
		// Owner rwx (scripts run as root), group/others at most r-x.
		mode := fs.FileMode(s.Mode)&0o755 | scriptsMinimumPerm
		path := filepath.Join(a.opts.ScriptsDir, s.Name)
		if local[s.Name] == s.SHA256 {
			if err := os.Chmod(path, mode); err != nil {
				return agentproto.SyncResult{}, err
			}
			continue
		}
		if err := writeFileAtomic(path, s.Content, mode); err != nil {
			return agentproto.SyncResult{}, fmt.Errorf("%s: %w", s.Name, err)
		}
		a.logger.Info("script updated", "script", s.Name, "sha256", s.SHA256)
	}
	for name := range local {
		if !keep[name] {
			if err := os.Remove(filepath.Join(a.opts.ScriptsDir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return agentproto.SyncResult{}, err
			}
			a.logger.Info("script removed", "script", name)
		}
	}
	return agentproto.SyncResult{Scripts: a.localScripts()}, nil
}

// validScriptName accepts a plain *.sh file name, without any path.
func validScriptName(name string) bool {
	return strings.HasSuffix(name, ".sh") && len(name) > len(".sh") && filepath.Base(name) == name &&
		!strings.ContainsAny(name, `/\`) && !strings.HasPrefix(name, ".")
}

// localScripts returns the *.sh files of the scripts directory: name → SHA-256.
func (a *Agent) localScripts() map[string]string {
	out := map[string]string{}
	entries, err := os.ReadDir(a.opts.ScriptsDir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.Type().IsRegular() || !validScriptName(e.Name()) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(a.opts.ScriptsDir, e.Name()))
		if err != nil {
			continue
		}
		out[e.Name()] = agentproto.Checksum(data)
	}
	return out
}

// writeFileAtomic writes data to a temporary file in the same directory, then
// renames it over path: a reader never sees a partial file.
func writeFileAtomic(path string, data []byte, perm fs.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
