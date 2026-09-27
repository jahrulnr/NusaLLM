// Package workspace confines reads and writes to one directory tree —
// attachment resolution and workspace-relative file creation for REPL
// commands, tools, and future web clients share this adapter.
package workspace

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// Root is an opened workspace directory (os.Root sandbox).
type Root struct {
	root    *os.Root
	absPath string
}

// Open resolves and opens a workspace root directory.
func Open(dir string) (*Root, error) {
	if dir == "" {
		dir = "."
	}
	absPath, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(absPath)
	if err != nil {
		return nil, err
	}
	return &Root{root: root, absPath: absPath}, nil
}

// FromRoot adopts an existing os.Root and absolute path.
func FromRoot(root *os.Root, absPath string) *Root {
	return &Root{root: root, absPath: absPath}
}

// Path returns the absolute workspace path.
func (w *Root) Path() string { return w.absPath }

// FS exposes the workspace for directory listing (e.g. command completion).
func (w *Root) FS() fs.FS { return w.root.FS() }

// CreateFile opens a new file inside the workspace.
func (w *Root) CreateFile(name string) (io.WriteCloser, error) {
	return w.root.Create(name)
}

// Close releases the underlying root handle.
func (w *Root) Close() error { return w.root.Close() }

// OSRoot exposes the sandbox for callers that still need os.Root (session
// send path); prefer Path/FS/Create/ResolveAttachment when possible.
func (w *Root) OSRoot() *os.Root { return w.root }
