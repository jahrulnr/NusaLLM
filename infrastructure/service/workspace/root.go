// Package workspace confines reads and writes to one directory tree —
// attachment resolution and workspace-relative file creation for REPL
// commands, tools, and future web clients share this adapter.
package workspace

import (
	"context"
	"errors"
	"io"
	"io/fs"

	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
)

// Root is an opened workspace directory through an injected store.
type Root struct {
	root    drepo.WorkspaceSession
	absPath string
}

// Open resolves and opens a workspace root directory.
func Open(store drepo.Workspace, dir string) (*Root, error) {
	if store == nil {
		return nil, errors.New("workspace repository is required")
	}
	if dir == "" {
		dir = "."
	}
	absPath, err := store.CheckRoot(dir)
	if err != nil {
		return nil, err
	}
	root, err := store.Open(context.Background(), absPath)
	if err != nil {
		return nil, err
	}
	return &Root{root: root, absPath: absPath}, nil
}

// Path returns the absolute workspace path.
func (w *Root) Path() string { return w.absPath }

// FS exposes the workspace for directory listing (e.g. command completion).
func (w *Root) FS() fs.FS { return w.root.FS() }

// CreateFile opens a new file inside the workspace.
func (w *Root) CreateFile(name string) (io.WriteCloser, error) {
	return w.root.CreateFile(name)
}

// Close releases the underlying root handle.
func (w *Root) Close() error { return w.root.Close() }
