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
