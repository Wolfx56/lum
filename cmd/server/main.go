// Command lum-server runs the LXC Update Manager server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Wolfx56/lum/internal/auth"
	"github.com/Wolfx56/lum/internal/config"
	"github.com/Wolfx56/lum/internal/logging"
	"github.com/Wolfx56/lum/internal/server"
	"github.com/Wolfx56/lum/internal/service"
	"github.com/Wolfx56/lum/internal/store"
	"github.com/Wolfx56/lum/internal/ui"
	"github.com/Wolfx56/lum/internal/version"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "lum-server:", err)
		os.Exit(1)
	}
}

func run() error {
	configDir := flag.String("config-dir", "config", "directory containing server.yaml and apps.yaml")
	envFile := flag.String("env-file", ".env", "optional .env file resolving ${VAR} references")
	listen := flag.String("listen", "", "HTTP listen address (overrides server.listen)")
	logLevel := flag.String("log-level", "", "log level: debug, info, warn, error (overrides server.log_level)")
	dataDir := flag.String("data-dir", "/data", "state directory (SQLite database lum.db)")
	agentDir := flag.String("agent-dir", "/opt/lum/agent", "agent binaries lum-agent-linux-<arch> served to agents (install, self_update)")
	flag.Parse()

	holder, err := config.NewHolder(config.Options{Dir: *configDir, EnvFile: *envFile})
	if err != nil {
		return fmt.Errorf("loading configuration from %s: %w", *configDir, err)
	}
	cfg := holder.Current()

	if *listen == "" {
		*listen = cfg.Server.Listen
	}
	if *logLevel == "" {
		*logLevel = cfg.Server.LogLevel
	}
	level, err := logging.ParseLevel(*logLevel)
	if err != nil {
		return err
	}
	logger := logging.New(os.Stdout, level)
	// Gin's debug mode prints plain-text lines; all logs must be JSON (spec §13).
	gin.SetMode(gin.ReleaseMode)
	logger.Info("configuration loaded", "dir", *configDir, "apps", len(cfg.Apps), "auth_mode", cfg.Auth.Mode)
	if cfg.Auth.Mode == config.AuthNone {
		logger.Warn("authentication disabled (auth.mode: none): use on a trusted network only")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := os.MkdirAll(*dataDir, 0o750); err != nil {
		return fmt.Errorf("creating data dir: %w", err)
	}
	dbFile := filepath.Join(*dataDir, "lum.db")
	db, err := store.Open(ctx, dbFile)
	if err != nil {
		return err
	}
	defer db.Close()
	logger.Info("database opened", "file", dbFile)

	svc, err := service.New(service.Options{Holder: holder, Store: db, Logger: logger, AgentDir: *agentDir})
	if err != nil {
		return err
	}
	defer svc.Stop()
	svc.Start()

	// SIGHUP reloads the configuration (spec §4); a failed reload keeps the previous one.
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	go func() {
		for range hup {
			logger.Info("SIGHUP received, reloading configuration")
			_ = svc.Reload()
		}
	}()

	// Settings below are read once: changing them requires a restart.
	authn := auth.New(auth.Options{Config: cfg.Auth, BaseURL: cfg.Server.BaseURL, Store: db, Logger: logger})
	uiFS := ui.FS()
	srv := &http.Server{
		Addr: *listen,
		Handler: server.New(server.Options{
			Logger: logger, UI: uiFS, Auth: authn, Service: svc, BaseURL: cfg.Server.BaseURL,
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("server listening", "addr", *listen, "version", version.Version, "ui_embedded", uiFS != nil)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
