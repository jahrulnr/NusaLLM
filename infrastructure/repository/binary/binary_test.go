package repobinary

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCommitReplacesInstalledBinaryWithExecutableBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nusallm")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := NewInstalled(path)
	s, err := b.Stage()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write([]byte("new")); err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "new" {
		t.Fatalf("installed = %q, err %v", got, err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %v, want 0755", fi.Mode().Perm())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("staging file left behind: %v", entries)
	}
}

func TestDiscardLeavesInstalledBinaryAlone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nusallm")
	if err := os.WriteFile(path, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := NewInstalled(path).Stage()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = s.Write([]byte("half"))
	if err := s.Discard(); err != nil {
		t.Fatalf("Discard: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "old" {
		t.Fatalf("installed changed on discard: %q", got)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("staging file left behind: %v", entries)
	}
}

func TestStagingEndsOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nusallm")
	s, err := NewInstalled(path).Stage()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(); err == nil {
		t.Fatal("second Commit succeeded")
	}
	if err := s.Discard(); err != nil {
		t.Fatalf("Discard after Commit should be a no-op: %v", err)
	}
}

func TestStageCreatesMissingDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "home", ".local", "bin", "nusallm")
	s, err := NewInstalled(path).Stage()
	if err != nil {
		t.Fatalf("Stage in a missing directory: %v", err)
	}
	if err := s.Discard(); err != nil {
		t.Fatal(err)
	}
}
