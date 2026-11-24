package repoworkspace

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	"github.com/jahrulnr/nusallm/infrastructure/system/patharg"
)

type Local struct{}

func NewLocal() drepo.Workspace { return Local{} }

func (Local) CheckRoot(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	root, err := os.OpenRoot(abs)
	if err != nil {
		return "", err
	}
	return abs, root.Close()
}

func (Local) Open(ctx context.Context, dir string) (drepo.WorkspaceSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("workspace %q: %w", dir, err)
	}
	return &session{root: root, dir: dir}, nil
}

// OpenHost reads a regular file by absolute host path — the workspace
// os.Root never enters the picture, and the sha256 pin remains the only
// guarantee the caller gets about what send-time serves.
func (Local) OpenHost(ctx context.Context, path string) (io.ReadCloser, fs.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if !filepath.IsAbs(path) {
		return nil, nil, fmt.Errorf("host path %q must be absolute", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		if err == nil {
			err = fmt.Errorf("path is not a regular file")
		}
		return nil, nil, err
	}
	return f, info, nil
}

// LocateRead answers where a read of raw lands: inside dir (a confined
// session plus the workspace-relative name) or on a regular host file by
// absolute path. The argument is expanded first, so a file: URL, a leading ~
// or a Windows %VAR% meets the same placement rule as any other path.
func (Local) LocateRead(ctx context.Context, dir, raw string) (drepo.Placement, error) {
	return locate(ctx, dir, raw, true)
}

// LocateWrite answers where a write of raw lands. A path outside dir is an
// error naming the workspace root: a read may opt into the host bypass, a
// write never does.
func (Local) LocateWrite(ctx context.Context, dir, raw string) (drepo.Placement, error) {
	return locate(ctx, dir, raw, false)
}

// locate applies the one placement rule. The expanded path is relative
// (resolved in dir), absolute inside dir (resolved relative to it, so
// os.Root still guards symlinks and ..), or absolute outside it — a host
// read, or a refusal for a write.
func locate(ctx context.Context, dir, raw string, hostReads bool) (drepo.Placement, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := patharg.Expand(raw)
	if err != nil {
		return nil, err
	}
	if path == "" {
		return nil, fmt.Errorf("path is required")
	}
	if dir == "" {
		return nil, fmt.Errorf("workspace root is required")
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	place := &localPlacement{root: root, raw: raw}
	if !filepath.IsAbs(path) {
		place.name = path
	} else {
		clean := filepath.Clean(path)
		if rel, relErr := filepath.Rel(root, clean); relErr == nil && insideRoot(rel) {
			place.name = rel
		} else if hostReads {
			place.host = true
			place.abs = clean
			return place, nil
		} else {
			return nil, fmt.Errorf("cannot write %q: it is outside the workspace root %q — bind that folder as the workspace or use exec", raw, root)
		}
	}
	session, err := (Local{}).Open(ctx, root)
	if err != nil {
		return nil, err
	}
	place.session = session
	return place, nil
}

// insideRoot reports whether rel names a location inside the root rather than
// the root's parent (or another volume).
func insideRoot(rel string) bool {
	return rel != ".." && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// localPlacement is a path resolved by the local driver: a workspace session
// plus a relative name, or an absolute host file.
type localPlacement struct {
	session drepo.WorkspaceSession
	name    string // workspace-relative name when session != nil
	root    string
	raw     string // the argument as received, for error messages
	host    bool
	abs     string
}

func (p *localPlacement) Open(ctx context.Context) (io.ReadCloser, fs.FileInfo, error) {
	if p.host {
		return (Local{}).OpenHost(ctx, p.abs)
	}
	return p.session.OpenRegular(p.name)
}

// Create replaces the placed file, creating parent directories (owner-only,
// matching the repository convention for key-bearing content). A host
// placement refuses: the placement rule never lets a write leave the
// workspace, so this only fires if a caller mixes the two.
func (p *localPlacement) Create(data []byte) error {
	if p.host {
		return fmt.Errorf("cannot write %q: it is outside the workspace root %q — bind that folder as the workspace or use exec", p.raw, p.root)
	}
	if info, err := p.session.Stat(p.name); err == nil && info.IsDir() {
		return fmt.Errorf("%q is a directory", p.raw)
	}
	if dir := filepath.Dir(p.name); dir != "." {
		if err := p.session.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	return p.session.WriteFile(p.name, data, 0o600)
}

func (p *localPlacement) URI() string {
	if p.host {
		return (Local{}).HostURI(p.abs)
	}
	return p.session.AttachmentURI(p.name)
}

func (p *localPlacement) Close() error {
	if p.session == nil {
		return nil
	}
	return p.session.Close()
}

func (Local) HostURI(path string) string { return "file://" + path }

type session struct {
	root *os.Root
	dir  string
}

func (s *session) Close() error { return s.root.Close() }

func (s *session) FS() fs.FS { return s.root.FS() }

func (s *session) CreateFile(name string) (io.WriteCloser, error) {
	// Create is OpenFile with mode 0666, i.e. whatever the umask lets
	// through. This path exports transcripts — pasted secrets travel
	// with them — so the file is owner-only regardless of the process
	// umask. Mode applies only when the name is created; truncating an
	// existing export leaves its old mode, so clamp the descriptor too.
	f, err := s.root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func (s *session) Stat(name string) (fs.FileInfo, error) { return s.root.Stat(name) }

func (s *session) OpenRegular(name string) (io.ReadCloser, fs.FileInfo, error) {
	f, err := s.root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, nil, fmt.Errorf("path is not a regular file")
	}
	return f, info, nil
}

func (s *session) ReadFile(name string) ([]byte, error) { return s.root.ReadFile(name) }

func (s *session) WriteFile(name string, data []byte, perm fs.FileMode) error {
	return s.root.WriteFile(name, data, perm)
}

func (s *session) MkdirAll(name string, perm fs.FileMode) error {
	return s.root.MkdirAll(name, perm)
}

func (s *session) AttachmentURI(name string) string {
	return "file://" + filepath.Join(s.dir, name)
}
