//go:build darwin || freebsd || netbsd || openbsd

package toolexec

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

const interruptSignal = syscall.SIGINT

// sessionCommand mirrors the linux path — same group-kill discipline.
func sessionCommand(command string) *exec.Cmd {
	cmd := exec.Command("sh", "-c", command)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd
}

var errPTYUnsupported = errors.New("tty sessions are unsupported on this platform")

// PTY allocation is linux-only for now — darwin/BSD use different ioctls
// (TIOCPTYGNAME/TIOCPTYUNLK). Pipe sessions still work; tty requests get
// a clear refusal instead of a silent fallback.
func openPTY() (master, slave *os.File, err error) { return nil, nil, errPTYUnsupported }

func ptyCommand(command string, slave *os.File) (*exec.Cmd, error) {
	return nil, errPTYUnsupported
}

func killGroup(pid int, sig syscall.Signal) error {
	if err := syscall.Kill(-pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

func (s *terminalSession) terminateProcess() {
	_ = killGroup(s.cmd.Process.Pid, syscall.SIGKILL)
}
