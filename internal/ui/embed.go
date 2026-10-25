//go:build embedui

// Package ui exposes the Svelte web UI build. It is embedded only when
// building with -tags embedui (after `make build-web`); otherwise FS is nil.
package ui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS returns the built UI rooted at its index.html.
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}
