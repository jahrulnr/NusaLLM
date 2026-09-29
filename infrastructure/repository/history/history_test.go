package repohistory

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// Escaping is the store's one tricky invariant: entries survive newlines
// and literal backslashes, and the unescape scan must stay single-pass — a
// chained replacer would turn an escaped literal \n into a newline.
func TestHistoryRoundTripPreservesEscapes(t *testing.T) {
	path, err := NewLocal().Open(filepath.Join(t.TempDir(), "cli"))
	if err != nil {
		t.Fatal(err)
	}
	entries := []string{"first", "with\nnewline", `literal\n`, `trailing\`, "back\\slash"}
	for _, entry := range entries {
		if err := NewLocal().Append(path, entry); err != nil {
			t.Fatal(err)
		}
	}
	if got := NewLocal().Load(path); !reflect.DeepEqual(got, entries) {
		t.Fatalf("round trip = %#v, want %#v", got, entries)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("history file mode = %v, want 0600", fi.Mode().Perm())
	}
	di, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("history dir mode = %v, want 0700", di.Mode().Perm())
	}
}

// The store refuses a missing directory, a symlinked directory, and a
// non-regular file where the history file belongs.
func TestHistoryOpenRequiresRealDirAndFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := NewLocal().Open(""); err == nil {
		t.Fatal("empty history directory accepted")
	}

	target := filepath.Join(dir, "real")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := NewLocal().Open(link); err == nil {
		t.Fatal("symlinked history directory accepted")
	}

	cli := filepath.Join(dir, "cli")
	if err := os.Mkdir(cli, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(cli, "history"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := NewLocal().Open(cli); err == nil {
		t.Fatal("directory in place of the history file accepted")
	}
}
