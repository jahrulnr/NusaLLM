//go:build linux

package tui

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jahrulnr/llm-base/infrastructure/delivery/cli/session"
	"golang.org/x/sys/unix"
)

// openPTY allocates a real pty pair — keys written to master arrive on
// slave exactly like a terminal emulator would deliver them. This is the
// only way to exercise bubbletea's raw-mode path: MakeRaw requires a
// real terminal fd.
func openPTY(t *testing.T) (master, slave *os.File) {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skip("no ptmx:", err)
	}
	var n uint
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, m.Fd(), unix.TIOCGPTN, uintptr(unsafe.Pointer(&n))); errno != 0 {
		t.Skip("TIOCGPTN:", errno)
	}
	var unlock int // TIOCSPTLCK takes an int*, not a flag
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, m.Fd(), unix.TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock))); errno != 0 {
		t.Skip("TIOCSPTLCK:", errno)
	}
	s, err := os.OpenFile("/dev/pts/"+strconv.Itoa(int(n)), os.O_RDWR, 0)
	if err != nil {
		t.Skip("open pts:", err)
	}
	return m, s
}

// TestTTYTypingReachesComposer guards the eofInput regression: the EOF
// injector wraps stdin but must still satisfy term.File — without Fd()
// bubbletea never calls MakeRaw, the tty stays cooked, and the renderer
// wipes each kernel-echoed key (typed text "disappears").
func TestTTYTypingReachesComposer(t *testing.T) {
	master, slave := openPTY(t)
	defer master.Close()
	defer slave.Close()

	var out safeBuffer
	input := &eofInput{rc: slave, inject: []byte("\n/exit\n")}
	m := newModel(&REPL{Session: &session.Session{Notify: func(string) {}},
		historyFile: t.TempDir() + "/h"}, true)
	m.ctx = context.Background()
	runtime := &tuiRuntime{}
	m.runtime = runtime
	program := tea.NewProgram(m, tea.WithInput(input), tea.WithOutput(&out))
	runtime.program = program

	done := make(chan error, 1)
	go func() {
		_, err := program.Run()
		done <- err
	}()
	t.Cleanup(func() { program.Kill() })
	time.Sleep(1200 * time.Millisecond) // renderer + raw mode settle

	if _, err := master.WriteString("hi"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	if got := out.String(); !strings.Contains(got, "❯ hi") {
		t.Fatalf("composer never received keystrokes; output=%q", got)
	}

	// Ctrl+C on a non-empty draft clears it; a second Ctrl+C quits.
	_, _ = master.WriteString("\x03")
	time.Sleep(300 * time.Millisecond)
	_, _ = master.WriteString("\x03")
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("program returned %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("program did not exit on ctrl+c")
	}
}
