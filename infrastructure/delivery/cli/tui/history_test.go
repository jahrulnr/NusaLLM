package tui

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPrivateHistoryFilePreservesContentsAndPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits are not enforced on Windows")
	}
	dir := filepath.Join(t.TempDir(), "cli")
	path, err := privateHistoryFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("previous prompt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := privateHistoryFile(dir); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "previous prompt\n" {
		t.Fatalf("history contents changed: %q", got)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0o700 || fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("history permissions dir=%04o file=%04o", dirInfo.Mode().Perm(), fileInfo.Mode().Perm())
	}
}

func TestPrivateHistoryFileRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	external := filepath.Join(t.TempDir(), "history")
	if err := os.WriteFile(external, []byte("do not touch"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(dir, "history")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := privateHistoryFile(dir); err == nil {
		t.Fatal("symlink history path should be rejected")
	}
	got, err := os.ReadFile(external)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "do not touch" {
		t.Fatalf("symlink target changed: %q", got)
	}
}
