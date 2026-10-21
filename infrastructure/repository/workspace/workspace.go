package repoworkspace

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
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

func (Local) HostURI(path string) string { return "file://" + path }

type session struct {
	root *os.Root
	dir  string
}

func (s *session) Close() error { return s.root.Close() }

func (s *session) FS() fs.FS { return s.root.FS() }

func (s *session) CreateFile(name string) (io.WriteCloser, error) {
	return s.root.Create(name)
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
