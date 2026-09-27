package valueobject

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
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

// ParseWorkspacePath accepts an optionally double-quoted path and validates it
// as a workspace-relative path.
func ParseWorkspacePath(raw string) (Workspace, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Workspace{}, errors.New("workspace path is required")
	}
	if raw[0] == '"' {
		path, err := strconv.Unquote(raw)
		if err != nil {
			return Workspace{}, fmt.Errorf("invalid quoted workspace path: %w", err)
		}
		raw = path
	}
	return NewWorkspace(raw)
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
