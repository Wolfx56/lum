// Package scripts indexes the central update scripts (scripts.dir, spec §9):
// every *.sh file with its SHA-256, kept up to date by an inotify watcher.
// See doc/agent.md.
package scripts

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/Wolfx56/lum/internal/agentproto"
)

// MaxSize is the largest script indexed; bigger files are skipped.
const MaxSize = 1 << 20

// debounce groups the events of one change (editor save, git pull).
const debounce = 300 * time.Millisecond

// Script is one indexed script.
type Script struct {
	Name    string
	SHA256  string
	Size    int64
	Mode    fs.FileMode
	Content []byte
}

// Index is the in-memory index of a scripts directory. It is safe for concurrent use.
type Index struct {
	logger *slog.Logger

	mu      sync.RWMutex
	dir     string
	scripts map[string]Script
}

// New indexes dir. A missing directory gives an empty index (the configuration
// validation already reports referenced scripts that do not exist).
func New(dir string, logger *slog.Logger) *Index {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	i := &Index{logger: logger, dir: dir}
	i.Rescan()
	return i
}

// Dir returns the indexed directory.
func (i *Index) Dir() string {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.dir
}

// Get returns one script.
func (i *Index) Get(name string) (Script, bool) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	s, ok := i.scripts[name]
	return s, ok
}

// SetDir switches to another directory (configuration reload) and rescans it.
func (i *Index) SetDir(dir string) bool {
	i.mu.Lock()
	changed := dir != i.dir
	i.dir = dir
	i.mu.Unlock()
	if changed {
		i.Rescan()
	}
	return changed
}

// Rescan rebuilds the index and reports whether it changed.
func (i *Index) Rescan() bool {
	dir := i.Dir()
	next, err := scan(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		i.logger.Warn("scripts directory unreadable", "dir", dir, "error", err.Error())
	}
	i.mu.Lock()
	changed := i.scripts == nil || !sameScripts(i.scripts, next)
	i.scripts = next
	i.mu.Unlock()
	if changed {
		i.logger.Info("scripts indexed", "dir", dir, "count", len(next))
		for name, msg := range skipped(dir, next) {
			i.logger.Warn("script skipped", "script", name, "reason", msg)
		}
	}
	return changed
}

func scan(dir string) (map[string]Script, error) {
	out := map[string]Script{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out, err
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".sh") || !e.Type().IsRegular() {
			continue
		}
		info, err := e.Info()
		if err != nil || info.Size() > MaxSize {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		out[name] = Script{Name: name, SHA256: agentproto.Checksum(data), Size: int64(len(data)), Mode: info.Mode().Perm(), Content: data}
	}
	return out, nil
}

// skipped lists the *.sh files that are not in the index (too big or unreadable).
func skipped(dir string, indexed map[string]Script) map[string]string {
	out := map[string]string{}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".sh") || !e.Type().IsRegular() {
			continue
		}
		if _, ok := indexed[name]; ok {
			continue
		}
		if info, err := e.Info(); err == nil && info.Size() > MaxSize {
			out[name] = fmt.Sprintf("larger than %d bytes", MaxSize)
		} else {
			out[name] = "unreadable"
		}
	}
	return out
}

func sameScripts(a, b map[string]Script) bool {
	if len(a) != len(b) {
		return false
	}
	for name, s := range a {
		if t, ok := b[name]; !ok || t.SHA256 != s.SHA256 || t.Mode != s.Mode {
			return false
		}
	}
	return true
}

// Watch rescans the directory on every change and calls onChange when the
// index changed, until ctx is cancelled. The directory may be replaced through
// SetDir: the watcher follows it at the next Watch tick.
func (i *Index) Watch(ctx context.Context, onChange func()) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close()

	watched := ""
	follow := func() {
		dir := i.Dir()
		if dir == watched {
			return
		}
		if watched != "" {
			_ = w.Remove(watched)
		}
		if err := w.Add(dir); err != nil {
			i.logger.Warn("scripts directory not watched", "dir", dir, "error", err.Error())
			watched = ""
			return
		}
		watched = dir
	}
	follow()

	// The ticker re-attaches the watcher when the directory is created later
	// or replaced, and is a safety net for missed events.
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	var timer <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return nil
		case _, ok := <-w.Events:
			if !ok {
				return nil
			}
			timer = time.After(debounce)
		case err, ok := <-w.Errors:
			if !ok {
				return nil
			}
			i.logger.Warn("scripts watcher error", "error", err.Error())
		case <-ticker.C:
			follow()
			timer = time.After(0)
		case <-timer:
			timer = nil
			if i.Rescan() && onChange != nil {
				onChange()
			}
		}
	}
}
