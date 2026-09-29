//go:build darwin || freebsd || netbsd || openbsd

package toolexec

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// shellCommand uses a process group so cancellation reaches the shell tree.
func shellCommand(ctx context.Context, command string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			if errors.Is(err, syscall.ESRCH) {
				return os.ErrProcessDone
			}
			return err
		}
		return nil
	}
	return cmd
}
