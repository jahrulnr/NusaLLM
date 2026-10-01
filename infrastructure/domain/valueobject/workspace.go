package valueobject

import (
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
)

var ErrPathTraversal = errors.New("path traversal is not allowed")

// Workspace is a workspace-relative path. Its grammar is platform-neutral —
// canonical slash-separated segments — because the workspace may live on a
// machine whose OS differs from the host's (ssh, docker environments), so
// host filepath semantics would validate the wrong alphabet.
type Workspace struct {
	path string
}

// NewWorkspace validates path as a workspace-relative location and stores its
// canonical form. A valid path is slash-separated: not empty, not rooted,
// without a drive-letter ("C:", "d:/") or UNC prefix, and without any ".."
// segment. Backslash is not a separator here — it is rejected outright so
// Windows-shaped input cannot smuggle a root or traversal past validation.
// "." and empty segments normalize away; ".." is rejected, never resolved.
func NewWorkspace(p string) (Workspace, error) {
	if p == "" {
		return Workspace{}, errors.New("workspace path is required")
	}
	if strings.IndexByte(p, '\\') >= 0 {
		return Workspace{}, fmt.Errorf("workspace path %q must use / separators", p)
	}
	// A leading ASCII letter + ":" is a Windows drive prefix — absolute
	// ("C:/x", "C:\x") or drive-relative ("C:x") — both escape the workspace.
	isDrive := len(p) >= 2 && p[1] == ':' &&
		(p[0] >= 'a' && p[0] <= 'z' || p[0] >= 'A' && p[0] <= 'Z')
	if p[0] == '/' || isDrive {
		return Workspace{}, ErrPathTraversal
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return Workspace{}, ErrPathTraversal
		}
	}
	return Workspace{path: path.Clean(p)}, nil
}

// ParseWorkspacePath accepts an optionally double-quoted path and validates it
// as a workspace-relative path.
func ParseWorkspacePath(raw string) (Workspace, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Workspace{}, errors.New("workspace path is required")
	}
	if raw[0] == '"' {
		unquoted, err := strconv.Unquote(raw)
		if err != nil {
			return Workspace{}, fmt.Errorf("invalid quoted workspace path: %w", err)
		}
		raw = unquoted
	}
	return NewWorkspace(raw)
}

// Path returns the canonical slash-separated path, the form fs.FS sessions
// and every workspace driver accept regardless of host OS.
func (w Workspace) Path() string {
	return w.path
}
