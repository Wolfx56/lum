// Package version exposes the build version, injected at link time:
//
//	go build -ldflags "-X github.com/Wolfx56/lum/internal/version.Version=1.2.3"
package version

// Version is the build version of the server and agent binaries.
var Version = "dev"
