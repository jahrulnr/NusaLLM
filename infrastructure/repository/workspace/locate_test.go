package repoworkspace

// T-01 — the workspace repository owns what a path argument means: syntax
// (file: URL, a leading ~, Windows %VAR%), placement inside or outside the
// root, and the read/write asymmetry.

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readPlacement reads the placed entry to a string.
func readPlacement(t *testing.T, place interface {
	Open(context.Context) (io.ReadCloser, os.FileInfo, error)
}) string {
	t.Helper()
	f, _, err := place.Open(context.Background())
	if err != nil {
		t.Fatalf("open placement: %v", err)
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("read placement: %v", err)
	}
	return string(data)
}

func TestLocateReadPlacesPaths(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "inside.txt"), []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "nested.txt"), []byte("nested"), 0o600); err != nil {
		t.Fatal(err)
	}
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, "probe.txt"), []byte("home"), 0o600); err != nil {
		t.Fatal(err)
	}

	store := NewLocal()
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"relative", "inside.txt", "inside"},
		{"relative nested", "sub/nested.txt", "nested"},
		{"absolute inside", filepath.Join(root, "inside.txt"), "inside"},
		{"absolute outside", outside, "outside"},
		{"home", "~/probe.txt", "home"},
		{"file url", "file://" + filepath.Join(root, "inside.txt"), "inside"},
		{"file url home", "file://" + filepath.Join(home, "probe.txt"), "home"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			place, err := store.LocateRead(context.Background(), root, tc.raw)
			if err != nil {
				t.Fatalf("LocateRead(%q): %v", tc.raw, err)
			}
			defer place.Close()
			if got := readPlacement(t, place); got != tc.want {
				t.Fatalf("content = %q, want %q", got, tc.want)
			}
		})
	}
}

// A relative name may not leave the workspace, and an absolute path inside
// it stays confined: both go through the session's os.Root.
func TestLocateReadRefusesEscapes(t *testing.T) {
	root := t.TempDir()
	outsideDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outsideDir, "secret.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewLocal()
	// An absolute path outside the root is a host *read* by contract, not an
	// escape — only names that resolve through the session are confined.
	for name, raw := range map[string]string{
		"parent":        "../secret.txt",
		"parent nested": "sub/../../secret.txt",
		"escaped dot":   "./../secret.txt",
	} {
		place, err := store.LocateRead(context.Background(), root, raw)
		if err == nil {
			err = func() error {
				defer place.Close()
				_, _, openErr := place.Open(context.Background())
				return openErr
			}()
		}
		if err == nil {
			t.Fatalf("%s: %q escaped the workspace", name, raw)
		}
	}
	// A symlink inside the root that points outside is refused by os.Root.
	if err := os.Symlink(outsideDir, filepath.Join(root, "link")); err == nil {
		place, err := store.LocateRead(context.Background(), root, "link/secret.txt")
		if err == nil {
			_, _, err = place.Open(context.Background())
			place.Close()
		}
		if err == nil {
			t.Fatal("symlink escaped the workspace")
		}
	}
}

// Writes never leave the workspace: an absolute out-of-root path is refused
// with the root named, and a ~/… target is refused before any literal
// folder can be created.
func TestLocateWriteRefusesOutside(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	outside := filepath.Join(t.TempDir(), "out.txt")
	store := NewLocal()

	for name, raw := range map[string]string{
		"absolute": outside,
		"home":     "~/probe.txt",
		"file url": "file://" + outside,
	} {
		_, err := store.LocateWrite(context.Background(), root, raw)
		if err == nil {
			t.Fatalf("%s: write placement accepted %q", name, raw)
		}
		if !strings.Contains(err.Error(), root) {
			t.Fatalf("%s: error %q must name the workspace root", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "~")); err == nil {
		t.Fatal(`write placement created a "./~" folder`)
	}
	if _, err := os.Stat(filepath.Join(home, "probe.txt")); err == nil {
		t.Fatal("write placement wrote into the home folder")
	}
}

// A write inside the workspace creates parents and the file, and its
// reference names the workspace path.
func TestLocateWriteCreatesInside(t *testing.T) {
	root := t.TempDir()
	store := NewLocal()
	place, err := store.LocateWrite(context.Background(), root, "nested/dir/a.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer place.Close()
	if err := place.Create([]byte("data")); err != nil {
		t.Fatalf("create: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "nested", "dir", "a.txt"))
	if err != nil || string(data) != "data" {
		t.Fatalf("written file = %q, %v", data, err)
	}
	if want := "file://" + filepath.Join(root, "nested", "dir", "a.txt"); place.URI() != want {
		t.Fatalf("uri = %q, want %q", place.URI(), want)
	}
	// An existing directory at the placement is reported as such.
	dirPlace, err := store.LocateWrite(context.Background(), root, "nested")
	if err != nil {
		t.Fatal(err)
	}
	defer dirPlace.Close()
	if err := dirPlace.Create([]byte("x")); err == nil || !strings.Contains(err.Error(), "is a directory") {
		t.Fatalf("directory placement create = %v, want a directory error", err)
	}
}
