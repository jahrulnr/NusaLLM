package toolexec

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// shellCommand runs command via sh -c. The child gets its own process
// group so Cancel can SIGKILL the whole tree — a plain Process.Kill
// would only reap the shell and orphan whatever it spawned.
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
