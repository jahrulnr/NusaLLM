package service

import (
	"context"
	"io"
)

// CommandRunner executes a shell command in the selected environment.
type CommandRunner interface {
	Run(ctx context.Context, command, workspace string, output io.Writer) (ProcessExit, error)
}

// ProcessExit keeps a nonzero process exit separate from a runner failure.
type ProcessExit struct {
	Code   int
	Signal string
	Err    error
}
