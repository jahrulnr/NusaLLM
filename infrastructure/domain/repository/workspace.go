package repository

import (
	"context"
	"io"
	"io/fs"
)

// Workspace provides storage access inside a selected workspace. A different
// implementation may place that workspace on another machine; tools do not
// open host files themselves.
type Workspace interface {
	CheckRoot(dir string) (string, error)
	Open(ctx context.Context, dir string) (WorkspaceSession, error)
	// OpenHost opens a regular file by absolute host path, bypassing the
	// workspace confinement — read tools opt in when a call names an
	// absolute path; writes never ride it. Remote drivers reject it: an
	// absolute host path is meaningless off-machine.
	OpenHost(ctx context.Context, path string) (io.ReadCloser, fs.FileInfo, error)
	// HostURI mints the file:// reference for a host-opened file — send-time
	// resolution re-opens it directly and still enforces the sha256 pin.
	HostURI(path string) string
}

type WorkspaceSession interface {
	FS() fs.FS
	CreateFile(name string) (io.WriteCloser, error)
	Stat(name string) (fs.FileInfo, error)
	OpenRegular(name string) (io.ReadCloser, fs.FileInfo, error)
	ReadFile(name string) ([]byte, error)
	WriteFile(name string, data []byte, perm fs.FileMode) error
	MkdirAll(name string, perm fs.FileMode) error
	AttachmentURI(name string) string
	Close() error
}
