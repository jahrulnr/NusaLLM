//go:build linux

package toolfile

import (
	"path/filepath"
	"syscall"
	"testing"
)

func TestFilePatchRejectsFIFOWithoutBlocking(t *testing.T) {
	root := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := mustPatchTool(t, root)
	if _, err := runPatch(t, tool, `{"path":"pipe","old_string":"a","new_string":"b"}`); err == nil {
		t.Fatal("FIFO accepted as patch target")
	}
}
