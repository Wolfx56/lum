//go:build !embedui

// Package ui exposes the Svelte web UI build. It is embedded only when
// building with -tags embedui (after `make build-web`); otherwise FS is nil.
package ui

import "io/fs"

// FS returns nil: the UI is not embedded (dev mode, served by the Vite dev server).
func FS() fs.FS { return nil }
