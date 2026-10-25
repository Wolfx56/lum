package config

import (
	"sync"
	"sync/atomic"
	"time"
)

// Holder keeps the active configuration and supports hot reload: a failed
// reload keeps the previous configuration active and records the error (spec §4).
type Holder struct {
	opts    Options
	current atomic.Pointer[Config]

	mu       sync.Mutex // serializes reloads and guards the fields below
	loadedAt time.Time
	lastErr  error
}

// NewHolder performs the initial load; an error here must block startup.
func NewHolder(opts Options) (*Holder, error) {
	cfg, err := Load(opts)
	if err != nil {
		return nil, err
	}
	h := &Holder{opts: opts, loadedAt: time.Now()}
	h.current.Store(cfg)
	return h, nil
}

// Current returns the active configuration. Callers must not modify it.
func (h *Holder) Current() *Config { return h.current.Load() }

// Reload re-reads the files. On error the active configuration is unchanged.
func (h *Holder) Reload() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	cfg, err := Load(h.opts)
	h.lastErr = err
	if err != nil {
		return err
	}
	h.current.Store(cfg)
	h.loadedAt = time.Now()
	return nil
}

// Status returns when the active configuration was loaded and the error of the
// last reload attempt (nil if it succeeded).
func (h *Holder) Status() (loadedAt time.Time, lastErr error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.loadedAt, h.lastErr
}
