package service

import (
	"context"
	"io"
)

// PythonRuntime reuses a working interpreter or provisions a private one.
// Provisioning progress goes to diagnostics, never the Python program's stdout.
type PythonRuntime interface {
	Ensure(ctx context.Context, diagnostics io.Writer) (string, error)
}
