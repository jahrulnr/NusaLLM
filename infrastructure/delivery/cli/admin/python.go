package admin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"

	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
)

// Python is the lazy interpreter boundary used by the terminal PATH launchers.
func Python(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, runtime dservice.PythonRuntime) int {
	executable, err := runtime.Ensure(ctx, stderr)
	if err != nil {
		fmt.Fprintln(stderr, "nusallm: Python:", err)
		return 1
	}
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() >= 0 {
			return exit.ExitCode()
		}
		fmt.Fprintln(stderr, "nusallm: Python:", err)
		return 1
	}
	return 0
}
