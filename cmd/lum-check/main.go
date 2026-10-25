// Command lum-check is a development tool: it loads the configuration, checks
// every app once (installed vs latest version) and prints the result. It does
// not persist anything or send notifications (the server's checker does).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/Wolfx56/lum/internal/checker"
	"github.com/Wolfx56/lum/internal/config"
	"github.com/Wolfx56/lum/internal/current"
	"github.com/Wolfx56/lum/internal/upstream"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "lum-check:", err)
		os.Exit(1)
	}
}

func run() error {
	configDir := flag.String("config-dir", "config", "directory containing server.yaml and apps.yaml")
	envFile := flag.String("env-file", ".env", "optional .env file resolving ${VAR} references")
	only := flag.String("app", "", "check only this app id")
	flag.Parse()

	cfg, err := config.Load(config.Options{Dir: *configDir, EnvFile: *envFile})
	if err != nil {
		return err
	}
	gh := upstream.NewGitHub(upstream.GitHubOptions{Token: cfg.GitHub.Token, CacheTTL: cfg.GitHub.CacheTTL})
	chk := checker.New(checker.Options{Upstream: gh, Current: current.NewReader(nil, nil)})

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "APP\tINSTALLED\tLATEST\tSTATUS\tDETAIL")
	for _, app := range cfg.Apps {
		if *only != "" && app.ID != *only {
			continue
		}
		res := chk.Collect(context.Background(), app)
		detail := res.Error
		if detail == "" {
			detail = res.ReleaseURL
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", app.ID, dash(res.CurrentVersion), dash(res.LatestVersion), res.Status, detail)
	}
	if err := w.Flush(); err != nil {
		return err
	}

	if rl := gh.RateLimit(); rl.Known {
		fmt.Printf("\nGitHub quota: %d/%d remaining, resets at %s\n", rl.Remaining, rl.Limit, rl.Reset.Format(time.TimeOnly))
	}
	return nil
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
