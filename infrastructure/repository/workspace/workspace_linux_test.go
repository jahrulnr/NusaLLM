//go:build linux

package repoworkspace

import (
	"path/filepath"
	"syscall"
	"testing"
)

func TestOpenRegularRejectsFIFOWithoutBlocking(t *testing.T) {
	rootPath := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(rootPath, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := NewLocal().Open(t.Context(), rootPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	if _, _, err := root.OpenRegular("pipe"); err == nil {
		t.Fatal("FIFO must be rejected as non-regular media")
	}
}
