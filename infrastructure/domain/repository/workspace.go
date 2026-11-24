package repository

import (
	"context"
	"io"
	"io/fs"
)

// Workspace provides storage access inside a selected workspace. A different
// implementation may place that workspace on another machine; tools do not
// open host files themselves.
//
// LocateRead and LocateWrite are the one rule for what a path argument means:
// syntax (a file: URL, a leading ~, a Windows %VAR%), placement relative to
// the workspace directory, and confinement. Callers pass the argument exactly
// as they received it and consume the Placement — they never expand, clean,
// join, or stat a path themselves.
type Workspace interface {
	CheckRoot(dir string) (string, error)
	Open(ctx context.Context, dir string) (WorkspaceSession, error)
	// LocateRead answers where a read of raw lands: inside the workspace
	// (confined, relative to dir) or on a regular host file by absolute
	// path. Reading outside the workspace is the deliberate bypass the
	// port documents; the argument may name a path that does not exist.
	LocateRead(ctx context.Context, dir, raw string) (Placement, error)
	// LocateWrite answers where a write of raw lands. A path outside the
	// workspace is an error naming the workspace root: writes never ride
	// the host-read bypass. Use exec to write elsewhere.
	LocateWrite(ctx context.Context, dir, raw string) (Placement, error)
	// OpenHost opens a regular file by absolute host path, bypassing the
	// workspace confinement — read tools opt in when a call names an
	// absolute path; writes never ride it. Remote drivers reject it: an
	// absolute host path is meaningless off-machine.
	OpenHost(ctx context.Context, path string) (io.ReadCloser, fs.FileInfo, error)
	// HostURI mints the file:// reference for a host-opened file — send-time
	// resolution re-opens it directly and still enforces the sha256 pin.
	HostURI(path string) string
}

// Placement is where a path argument landed. The driver that resolved it owns
// how it is opened, referenced, and created, so a remote driver needs no host
// filesystem behind the port. A Placement is single-use: Close releases its
// session handle.
type Placement interface {
	// Open opens the placed entry as a regular file for reading. A
	// directory or a missing file is an error.
	Open(ctx context.Context) (io.ReadCloser, fs.FileInfo, error)
	// Create replaces the placed entry with data, creating parent
	// directories as needed. A directory at the placement is an error.
	Create(data []byte) error
	// URI is the reference the placed file travels as (file:// for a local
	// workspace, a driver-specific reference otherwise).
	URI() string
	// Close releases the placement's session handle.
	Close() error
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
