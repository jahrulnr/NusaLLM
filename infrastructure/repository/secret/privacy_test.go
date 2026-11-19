package reposecret

import (
	"os"
	"path/filepath"
	"testing"
)

// New must not silently chmod a pre-existing caller-owned directory. A
// storage path may legitimately sit in a shared parent (a mount root, /tmp,
// a CI workspace); changing that parent to 0700 breaks every other user
// and process. A dedicated private directory should be created, or New
// should refuse the unsafe parent, not mutate it.
func TestNewDoesNotChmodPreexistingParent(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := New(filepath.Join(parent, "secrets.db"))
	if err != nil {
		return // refusing an unsafe shared parent is the other safe outcome
	}
	t.Cleanup(func() { _ = s.Close() })
	fi, err := os.Stat(parent)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o755 {
		t.Fatalf("pre-existing parent mode changed to %o", fi.Mode().Perm())
	}
}
