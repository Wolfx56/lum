// Command lum-agent runs the LUM agent inside an application LXC.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/Wolfx56/lum/internal/agent"
	"github.com/Wolfx56/lum/internal/logging"
	"github.com/Wolfx56/lum/internal/version"
)

// secretEnv holds the bootstrap secret: an environment variable (systemd
// EnvironmentFile) rather than a flag, which would be visible in ps.
const secretEnv = "LUM_AGENT_SECRET"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "lum-agent:", err)
		os.Exit(1)
	}
}

func run() error {
	id := flag.String("id", "", "agent id (as referenced by the agent field of apps.yaml)")
	serverURL := flag.String("server", "", "LUM server base URL, e.g. https://lum.example.com (http:// only towards localhost)")
	tokenFile := flag.String("token-file", "/opt/lum-agent/token", "agent token, written at enrollment")
	scriptsDir := flag.String("scripts-dir", "/opt/lum-agent/scripts", "local copies of the update scripts")
	logLevel := flag.String("log-level", "info", "log level: debug, info, warn, error")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("lum-agent", version.Version)
		return nil
	}
	level, err := logging.ParseLevel(*logLevel)
	if err != nil {
		return err
	}
	logger := logging.New(os.Stdout, level).With("agent_version", version.Version)

	a, err := agent.New(agent.Options{
		ID:         *id,
		ServerURL:  *serverURL,
		Secret:     os.Getenv(secretEnv),
		TokenFile:  *tokenFile,
		ScriptsDir: *scriptsDir,
		Logger:     logger,
	})
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return a.Run(ctx)
}
