package valueobject

import (
	"errors"
	"path/filepath"
)

var ErrPathTraversal = errors.New("path traversal is not allowed")

type Workspace struct {
	path string
}

func NewWorkspace(path string) (Workspace, error) {
	path = filepath.Clean(path)

	workspace := Workspace{
		path: path,
	}

	if err := workspace.validate(); err != nil {
		return Workspace{}, err
	}

	return workspace, nil
}

func (w Workspace) validate() error {
	if w.path == ".." || filepath.IsAbs(w.path) {
		return ErrPathTraversal
	}

	sep := string(filepath.Separator)
	if len(w.path) > 2 && w.path[:2] == ".." && w.path[2:3] == sep {
		return ErrPathTraversal
	}

	return nil
}

func (w Workspace) Path() string {
	return w.path
}
