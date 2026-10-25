package scripts

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Wolfx56/lum/internal/agentproto"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestIndex(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a.sh"), "echo a")
	write(t, filepath.Join(dir, "notes.txt"), "ignored")
	if err := os.WriteFile(filepath.Join(dir, "big.sh"), make([]byte, MaxSize+1), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "dir.sh"), 0o755); err != nil {
		t.Fatal(err)
	}

	idx := New(dir, nil)
	s, ok := idx.Get("a.sh")
	if !ok || s.SHA256 != agentproto.Checksum([]byte("echo a")) || s.Size != 6 || string(s.Content) != "echo a" {
		t.Fatalf("a.sh = %+v, %v", s, ok)
	}
	for _, name := range []string{"notes.txt", "big.sh", "dir.sh"} {
		if _, ok := idx.Get(name); ok {
			t.Errorf("%s must not be indexed", name)
		}
	}
	if idx.Rescan() {
		t.Error("rescan without change must report no change")
	}
	write(t, filepath.Join(dir, "a.sh"), "echo a2")
	if !idx.Rescan() {
		t.Error("modified script must be reported")
	}

	other := t.TempDir()
	write(t, filepath.Join(other, "b.sh"), "echo b")
	idx.SetDir(other)
	if _, ok := idx.Get("a.sh"); ok {
		t.Error("index must follow the new directory")
	}
	if _, ok := idx.Get("b.sh"); !ok {
		t.Error("b.sh must be indexed after SetDir")
	}

	if missing := New(filepath.Join(dir, "absent"), nil); len(missing.scripts) != 0 {
		t.Error("a missing directory gives an empty index")
	}
}

func TestWatch(t *testing.T) {
	dir := t.TempDir()
	idx := New(dir, nil)
	changed := make(chan struct{}, 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = idx.Watch(ctx, func() { changed <- struct{}{} }) }()
	time.Sleep(100 * time.Millisecond) // let the watcher start

	write(t, filepath.Join(dir, "new.sh"), "echo new")
	select {
	case <-changed:
	case <-time.After(5 * time.Second):
		t.Fatal("no change notified after adding a script")
	}
	if _, ok := idx.Get("new.sh"); !ok {
		t.Error("new.sh must be indexed")
	}
}
