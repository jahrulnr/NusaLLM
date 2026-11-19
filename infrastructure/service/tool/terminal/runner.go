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
	// WaitDelay fires when a descendant still holds the inherited pipe
	// after the leader has exited. A successful process state is the
	// leader's real status — the delay is not a command failure.
	if errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.Success() {
		return dservice.ProcessExit{Code: 0}, nil
	}
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		if code := exitErr.ExitCode(); code > 0 {
			return dservice.ProcessExit{Code: code}, nil
		}
		return dservice.ProcessExit{Signal: exitErr.Error()}, nil
	}
	return dservice.ProcessExit{Err: err}, nil
}
