//go:build windows

package toolexec

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// Windows has no portable signal model — Interrupt reports unsupported;
// the pipes backend's stdin still carries everything else.
const interruptSignal = syscall.Signal(2)

func sessionCommand(command string) *exec.Cmd {
	return exec.Command("cmd", "/C", command)
}

var errPTYUnsupported = errors.New("tty sessions are unsupported on windows")

func openPTY() (master, slave *os.File, err error) { return nil, nil, errPTYUnsupported }

func ptyCommand(command string, slave *os.File) (*exec.Cmd, error) {
	return nil, errPTYUnsupported
}

func killGroup(pid int, sig syscall.Signal) error {
	return errors.New("process-group signals are unsupported on windows")
}

func (s *terminalSession) terminateProcess() {
	_ = s.cmd.Process.Kill()
}
