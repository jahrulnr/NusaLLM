package service

import (
	"context"
	"io"
)

// CommandRunner executes a shell command in the selected environment.
// stdin feeds the process's standard input — nil means the null device:
// one-shot input only, never an interactive session.
type CommandRunner interface {
	Run(ctx context.Context, command, workspace string, stdin io.Reader, output io.Writer) (ProcessExit, error)
}

// ProcessExit keeps a nonzero process exit separate from a runner failure.
type ProcessExit struct {
	Code   int
	Signal string
	Err    error
}
