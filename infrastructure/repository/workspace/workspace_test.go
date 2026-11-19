package repoworkspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalWorkspaceConfinesStorage(t *testing.T) {
	root := t.TempDir()
	store := NewLocal()
	abs, err := store.CheckRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	session, err := store.Open(context.Background(), abs)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if err := session.WriteFile("child.txt", []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if data, err := session.ReadFile("child.txt"); err != nil || string(data) != "inside" {
		t.Fatalf("read = %q, %v", data, err)
	}
	if _, err := session.ReadFile("../outside.txt"); err == nil {
		t.Fatal("traversal escaped workspace")
	}
	if err := os.Symlink(filepath.Dir(root), filepath.Join(root, "link")); err == nil {
		if _, err := session.ReadFile("link/outside.txt"); err == nil {
			t.Fatal("symlink escaped workspace")
		}
	}
}

func TestLocalWorkspaceOpenHost(t *testing.T) {
	store := NewLocal()
	dir := t.TempDir()
	path := filepath.Join(dir, "host.bin")
	if err := os.WriteFile(path, []byte("host bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, info, err := store.OpenHost(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != int64(len("host bytes")) || info.Name() != "host.bin" {
		t.Fatalf("info = %v", info)
	}
	if got := store.HostURI(path); got != "file://"+path {
		t.Fatalf("uri = %q", got)
	}
	f.Close()
	// Non-regular and non-absolute inputs never produce a handle.
	if _, _, err := store.OpenHost(context.Background(), dir); err == nil {
		t.Fatal("directory must not open")
	}
	if _, _, err := store.OpenHost(context.Background(), "relative.bin"); err == nil {
		t.Fatal("relative path must not open")
	}
}

// Exported transcripts can contain pasted secrets. CreateFile is the
// export path, so it must create owner-only files rather than relying on
// the process umask.
func TestLocalWorkspaceCreateFileIsPrivate(t *testing.T) {
	root := t.TempDir()
	store := NewLocal()
	abs, err := store.CheckRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	session, err := store.Open(context.Background(), abs)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	f, err := session.CreateFile("export.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("private")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(root, "export.md"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("exported file is group/world accessible: %o", fi.Mode().Perm())
	}
}

// An export overwrites an existing path. A file left at 0644 by an older
// export (or another tool) must be re-clamped when this owner-only path
// truncates it; mode-on-create alone leaves the new transcript readable
// by group/other.
func TestLocalWorkspaceCreateFileReclampsExisting(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, "export.md")
	if err := os.WriteFile(old, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(old, 0o644); err != nil {
		t.Fatal(err)
	}
	store := NewLocal()
	abs, err := store.CheckRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	session, err := store.Open(context.Background(), abs)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	f, err := session.CreateFile("export.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("new")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(old)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("existing export remained group/world accessible: %o", fi.Mode().Perm())
	}
}
