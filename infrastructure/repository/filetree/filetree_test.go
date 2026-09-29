package repofiletree

import (
	"os"
	"path/filepath"
	"testing"
)

// The local file tree is a pass-through over the host filesystem — the
// picker's contract is exactly os.Stat / os.ReadDir / os.UserHomeDir.
func TestLocalFileTreeDelegatesToHostFilesystem(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tree := NewLocal()

	info, err := tree.Stat(filepath.Join(dir, "a.txt"))
	if err != nil || info.IsDir() || info.Size() != 1 {
		t.Fatalf("Stat = %v, %v", info, err)
	}
	if _, err := tree.Stat(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("Stat(missing) must error")
	}

	entries, err := tree.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "a.txt" {
		t.Fatalf("ReadDir = %v, %v", entries, err)
	}
	if _, err := tree.ReadDir(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("ReadDir(missing) must error")
	}

	home, err := tree.HomeDir()
	if err != nil || !filepath.IsAbs(home) {
		t.Fatalf("HomeDir = %q, %v", home, err)
	}
}
