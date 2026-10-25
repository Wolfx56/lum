package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"go.yaml.in/yaml/v3"
)

// File names inside the config directory.
const (
	ServerFile = "server.yaml"
	AppsFile   = "apps.yaml"
)

// Options tells Load where to find the configuration.
type Options struct {
	// Dir contains server.yaml and apps.yaml.
	Dir string
	// EnvFile is an optional .env file; a missing file is ignored.
	EnvFile string
	// LookupEnv overrides os.LookupEnv (tests). The process environment wins over EnvFile.
	LookupEnv LookupFunc
}

// Load reads, resolves and validates the configuration. The returned error is a
// *MissingEnvError, a *ValidationError, or a read/parse error naming the file.
func Load(opts Options) (*Config, error) {
	cfg, err := decodeFiles(opts.Dir)
	if err != nil {
		return nil, err
	}
	applyDefaults(cfg)

	lookup := opts.LookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}
	var fileVars map[string]string
	if opts.EnvFile != "" {
		if fileVars, err = readEnvFile(opts.EnvFile); err != nil {
			return nil, fmt.Errorf("reading env file: %w", err)
		}
	}
	if err := substituteEnv(cfg, envLookup(lookup, fileVars)); err != nil {
		return nil, err
	}

	if cfg.Scripts.Dir != "" && !filepath.IsAbs(cfg.Scripts.Dir) {
		cfg.Scripts.Dir = filepath.Join(opts.Dir, cfg.Scripts.Dir)
	}
	if abs, err := filepath.Abs(cfg.Scripts.Dir); err == nil {
		cfg.Scripts.Dir = abs
	}

	if err := validate(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// decodeFiles strictly decodes server.yaml and apps.yaml (unknown fields are errors).
func decodeFiles(dir string) (*Config, error) {
	// Global defaults are set before decoding: YAML only overwrites the fields
	// present in the file, so an explicit zero (jitter: 0s) is kept.
	cfg := globalDefaults()
	if err := decodeFile(filepath.Join(dir, ServerFile), &cfg); err != nil {
		return nil, err
	}
	if len(cfg.Apps) > 0 {
		return nil, fmt.Errorf("%s: apps must be defined in %s", ServerFile, AppsFile)
	}

	var apps struct {
		Apps []App `yaml:"apps"`
	}
	if err := decodeFile(filepath.Join(dir, AppsFile), &apps); err != nil {
		return nil, err
	}
	cfg.Apps = apps.Apps
	return &cfg, nil
}

func decodeFile(path string, out any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return nil
}

// Default values for fields left empty (documented in doc/configuration.md).
const (
	DefaultListen            = ":8080"
	DefaultLogLevel          = "info"
	DefaultSessionTTL        = 12 * time.Hour
	DefaultCacheTTL          = 15 * time.Minute
	DefaultCheckCron         = "0 7 * * *"
	DefaultJitter            = 5 * time.Minute
	DefaultConcurrency       = 5
	DefaultHeartbeatInterval = 60 * time.Second
	DefaultOfflineAfter      = 5 * time.Minute
	DefaultScriptsDir        = "/data/scripts"
	DefaultTagRegex          = `^v?(?P<version>\d+\.\d+\.\d+(\.\d+)?)$`
	DefaultUpdateMethod      = "POST"
	DefaultUpdateTimeout     = 15 * time.Minute
	DefaultExpectStatus      = 200
	DefaultHealthTimeout     = 120 * time.Second
	DefaultRetention         = 2
)

func globalDefaults() Config {
	return Config{
		Server:    Server{Listen: DefaultListen, LogLevel: DefaultLogLevel},
		Auth:      Auth{Mode: AuthOIDC, SessionTTL: DefaultSessionTTL},
		GitHub:    GitHub{CacheTTL: DefaultCacheTTL},
		Scheduler: Scheduler{CheckCron: DefaultCheckCron, Jitter: DefaultJitter, Concurrency: DefaultConcurrency},
		Agents: Agents{
			HeartbeatInterval: DefaultHeartbeatInterval,
			OfflineAfter:      DefaultOfflineAfter,
			Enrollment:        EnrollmentOpen,
			SelfUpdate:        SelfUpdateAuto,
		},
		Scripts: Scripts{Dir: DefaultScriptsDir},
	}
}

// applyDefaults fills the app fields left empty. For these fields the zero
// value is never valid, so "empty" and "absent" mean the same thing.
func applyDefaults(c *Config) {
	for i := range c.Apps {
		a := &c.Apps[i]
		setDefault(&a.Name, a.ID)
		setDefault(&a.Retention, DefaultRetention)
		setDefault(&a.Upstream.TagRegex, DefaultTagRegex)
		if u := a.Update; u != nil {
			if u.Type == TypeAPI {
				setDefault(&u.Method, DefaultUpdateMethod)
			}
			setDefault(&u.Timeout, DefaultUpdateTimeout)
		}
		if h := a.Healthcheck; h != nil {
			setDefault(&h.ExpectStatus, DefaultExpectStatus)
			setDefault(&h.Timeout, DefaultHealthTimeout)
		}
	}
}

func setDefault[T comparable](field *T, def T) {
	var zero T
	if *field == zero {
		*field = def
	}
}
