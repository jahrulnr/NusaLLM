package toolexec

import (
	"context"
	"errors"
	"io"
	"os/exec"

	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
)

type LocalRunner struct{ environment dservice.CommandEnvironment }

func NewLocalRunner(environment dservice.CommandEnvironment) dservice.CommandRunner {
	return LocalRunner{environment: environment}
}

func (r LocalRunner) Run(ctx context.Context, command, workspace string, stdin io.Reader, output io.Writer) (dservice.ProcessExit, error) {
	cmd := shellCommand(ctx, command)
	cmd.WaitDelay = waitDelay
	cmd.Dir = workspace
	if r.environment != nil {
		cmd.Env = r.environment.Environ(workspace)
	}
	if stdin != nil {
		cmd.Stdin = stdin
	}
	// Sharing the writer makes os/exec merge stdout and stderr in arrival order.
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Start(); err != nil {
		return dservice.ProcessExit{}, err
	}
	err := cmd.Wait()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if code := exitErr.ExitCode(); code > 0 {
			return dservice.ProcessExit{Code: code}, nil
		}
		return dservice.ProcessExit{Signal: exitErr.Error()}, nil
	}
	return dservice.ProcessExit{Err: err}, nil
}
