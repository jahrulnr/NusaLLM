//go:build linux

package wire

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestOpenRegularFileRejectsFIFOWithoutBlocking(t *testing.T) {
	rootPath := t.TempDir()
	fifo := filepath.Join(rootPath, "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })

	if _, _, err := OpenRegularFile(root, "pipe"); err == nil {
		t.Fatal("FIFO must be rejected as non-regular media")
	}
}
