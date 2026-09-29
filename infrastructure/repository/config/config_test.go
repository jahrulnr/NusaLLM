package repoconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// WriteAtomic must leave exactly the new document behind: parent created,
// an existing file's mode preserved, and no staged tmp files.
func TestWriteAtomicCreatesParentAndKeepsMode(t *testing.T) {
	target := filepath.Join(t.TempDir(), "nested", "config.json")
	if err := NewLocal().WriteAtomic(target, []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != `{"a":1}` {
		t.Fatalf("write = %q, %v", got, err)
	}
	if err := os.Chmod(target, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := NewLocal().WriteAtomic(target, []byte(`{"a":2}`)); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600 preserved", fi.Mode().Perm())
	}
	entries, err := os.ReadDir(filepath.Dir(target))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Fatalf("staged tmp file left behind: %s", e.Name())
		}
	}
}

// A symlinked path is followed: the real file receives the bytes and the
// link survives.
func TestWriteAtomicFollowsSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.json")
	if err := os.WriteFile(real, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := NewLocal().WriteAtomic(link, []byte(`{"b":2}`)); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink replaced instead of followed: %v", err)
	}
	if got, err := os.ReadFile(real); err != nil || string(got) != `{"b":2}` {
		t.Fatalf("real file = %q, %v", got, err)
	}
}

func TestExistsAndRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	exists, err := NewLocal().Exists(path)
	if err != nil || exists {
		t.Fatalf("Exists(missing) = %v, %v", exists, err)
	}
	if _, err := NewLocal().Read(path); err == nil {
		t.Fatal("Read(missing) must error")
	}
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	exists, err = NewLocal().Exists(path)
	if err != nil || !exists {
		t.Fatalf("Exists(present) = %v, %v", exists, err)
	}
	if raw, err := NewLocal().Read(path); err != nil || string(raw) != "{}" {
		t.Fatalf("Read = %q, %v", raw, err)
	}
}
