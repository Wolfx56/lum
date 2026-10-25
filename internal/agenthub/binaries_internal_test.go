package agenthub

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Wolfx56/lum/internal/agentproto"
)

func TestBinariesUnavailable(t *testing.T) {
	dir := t.TempDir()
	b := &binaries{dir: dir}
	if got := (&binaries{}).unavailable("amd64"); !strings.Contains(got, "--agent-dir") {
		t.Errorf("no dir: %q", got)
	}
	if got := b.unavailable("amd64"); !strings.Contains(got, "no such file") {
		t.Errorf("missing file: %q", got)
	}

	// The case met in production: the file is there but its directory cannot be traversed.
	if os.Geteuid() == 0 {
		t.Skip("root ignores permissions")
	}
	sub := filepath.Join(dir, "agent")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, agentproto.BinaryName("amd64")), []byte("bin"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sub, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })
	b = &binaries{dir: sub}
	if _, ok := b.get("amd64"); ok {
		t.Fatal("binary in a non-traversable directory must be unavailable")
	}
	if got := b.unavailable("amd64"); !strings.Contains(got, "permission denied") {
		t.Errorf("non-traversable dir: %q", got)
	}
}
