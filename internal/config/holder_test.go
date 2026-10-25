package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

const minimalServer = "auth:\n  mode: none\n"

func appsWith(id string) string {
	return `apps:
  - id: ` + id + `
    lxc_id: 1
    upstream: {type: github, repo: "a/b"}
    current: {type: api, url: "http://192.0.2.1/status", json_path: "$.version"}
`
}

func TestHolderReload(t *testing.T) {
	dir := writeConfig(t, map[string]string{ServerFile: minimalServer, AppsFile: appsWith("first")})
	h, err := NewHolder(Options{Dir: dir, LookupEnv: fakeEnv(nil)})
	if err != nil {
		t.Fatalf("NewHolder: %v", err)
	}

	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, AppsFile), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// An invalid file keeps the previous configuration active.
	write(appsWith("Not Kebab"))
	var verr *ValidationError
	if err := h.Reload(); !errors.As(err, &verr) {
		t.Fatalf("Reload: want *ValidationError, got %v", err)
	}
	if _, ok := h.Current().App("first"); !ok {
		t.Fatal("previous configuration must stay active after a failed reload")
	}
	if _, lastErr := h.Status(); lastErr == nil {
		t.Error("Status must report the failed reload")
	}

	// A valid file replaces it and clears the error.
	write(appsWith("second"))
	if err := h.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if _, ok := h.Current().App("second"); !ok {
		t.Fatal("new configuration not active")
	}
	if _, lastErr := h.Status(); lastErr != nil {
		t.Errorf("Status after a good reload: %v", lastErr)
	}
}

func TestNewHolderFailsOnInvalidConfig(t *testing.T) {
	dir := writeConfig(t, map[string]string{ServerFile: minimalServer, AppsFile: appsWith("Bad ID")})
	if _, err := NewHolder(Options{Dir: dir, LookupEnv: fakeEnv(nil)}); err == nil {
		t.Fatal("NewHolder must fail on an invalid configuration")
	}
}
