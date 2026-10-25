package repository

import (
	"context"
	"io"
)

// PythonRuntime stores standalone distributions. A staged installation is
// invisible to Cached until its executable has passed the service probe.
type PythonRuntime interface {
	Cached() (string, error)
	Stage(ctx context.Context, archive io.Reader) (PythonInstallation, error)
}

type PythonInstallation interface {
	Executable() string
	Commit() error
	Close() error
}
