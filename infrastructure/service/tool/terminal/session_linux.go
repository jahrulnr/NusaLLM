//go:build linux

package toolexec

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

const interruptSignal = syscall.SIGINT

// sessionCommand is shellCommand minus the ctx binding — a session
// outlives its opening call, so exec.CommandContext is not used. Setpgid
// keeps the kill discipline identical: the child leads its own group and
// killGroup reaches the whole tree.
func sessionCommand(command string) *exec.Cmd {
	cmd := exec.Command("sh", "-c", command)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd
}

// ptyCommand binds the slave side to 0/1/2 in a fresh session whose
// controlling terminal is that slave — Setsid makes the child a session
// leader (and process-group leader, so killGroup still reaches the
// tree), Setctty/Ctty:0 attaches stdin as the ctty.
func ptyCommand(command string, slave *os.File) (*exec.Cmd, error) {
	cmd := exec.Command("sh", "-c", command)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	return cmd, nil
}

// openPTY allocates a pseudo-terminal pair: master stays with the parent
// for reads and writes, slave goes to the child. Same recipe as codex's
// open_unix_pty — unlock via TIOCSPTLCK, resolve the number via TIOCGPTN,
// open /dev/pts/N without making it our own ctty.
func openPTY() (master, slave *os.File, err error) {
	master, err = os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, err
	}
	fail := func(e error) (*os.File, *os.File, error) {
		master.Close()
		return nil, nil, e
	}
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		return fail(err)
	}
	n, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		return fail(err)
	}
	slave, err = os.OpenFile("/dev/pts/"+strconv.Itoa(n), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		return fail(err)
	}
	// A sane initial geometry — programs querying winsize get an answer
	// instead of 0x0. Best-effort: failure does not block the session.
	_ = unix.IoctlSetWinsize(int(master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 24, Col: 80})
	return master, slave, nil
}

// killGroup signals the whole process group — -pid targets the group the
// child leads. ESRCH means the tree is already gone, which is success.
func killGroup(pid int, sig syscall.Signal) error {
	if err := syscall.Kill(-pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

func (s *terminalSession) terminateProcess() {
	_ = killGroup(s.cmd.Process.Pid, syscall.SIGKILL)
}
