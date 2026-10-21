package toolexec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
)

const (
	// sessionBufferCap bounds undrained output per session — the rolling
	// tail keeps the newest bytes; dropped head bytes are counted into
	// the omitted marker, same contract as codex's HeadTailBuffer.
	sessionBufferCap = 1 << 20
	// postExitDrainWait gives the reader goroutine a beat after the
	// process exits so trailing output is collected, mirroring codex's
	// POST_EXIT_CLOSE_WAIT_CAP.
	postExitDrainWait = 50 * time.Millisecond
)

// scrubEnv keeps session output textual and deterministic — codex's
// UNIFIED_EXEC_ENV set: a dumb TERM and cat pagers stop escape-heavy
// interactive UI, the C.UTF-8 locale pins output encoding, and
// NUSALLM_CI marks the environment the way CODEX_CI marks codex's.
var scrubEnv = map[string]string{
	"NO_COLOR":   "1",
	"TERM":       "dumb",
	"LANG":       "C.UTF-8",
	"LC_CTYPE":   "C.UTF-8",
	"LC_ALL":     "C.UTF-8",
	"COLORTERM":  "",
	"PAGER":      "cat",
	"GIT_PAGER":  "cat",
	"GH_PAGER":   "cat",
	"NUSALLM_CI": "1",
}

// terminalSession is one long-lived child. Its reader and waiter
// goroutines belong to the session — never to a caller ctx — so an
// interrupted turn leaves the process running. Terminate is the only
// reaper; it is idempotent and safe to call from any goroutine.
type terminalSession struct {
	id  int
	tty bool
	cmd *exec.Cmd
	env []string
	dir string

	// master serves the pty backend for both reads and writes; pipes
	// sessions hold no stdin handle at all — their child reads EOF.
	master *os.File

	done      chan struct{} // closed when Wait() returns
	readerEOF chan struct{} // closed when the output stream hits EOF
	notify    chan struct{} // cap 1 — output appended
	// interaction is a cap-1 semaphore serializing store-level
	// write/poll interactions — codex's interaction_lock: calls on one
	// session share the draining buffer, so they queue instead of racing.
	interaction chan struct{}
	exitCode    int // written before done closes
	exited      atomic.Bool
	lastUsed    atomic.Int64 // unixnano — store's LRU clock
	terminated  atomic.Bool

	mu      sync.Mutex // pending + omitted
	writeMu sync.Mutex // serialize stdin/master writes
	pending []byte     // undrained output — rolling tail window
	omitted int        // bytes dropped from pending's head
}

// sessionEnv builds the child environment: the daemon env minus the
// scrubbed keys, then the scrub values — explicit overwrites beat relying
// on last-entry-wins semantics.
func sessionEnv() []string {
	env := make([]string, 0, len(os.Environ())+len(scrubEnv))
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		if _, scrubbed := scrubEnv[key]; !scrubbed {
			env = append(env, kv)
		}
	}
	for k, v := range scrubEnv {
		env = append(env, k+"="+v)
	}
	return env
}

// spawnSession starts the child detached from any caller ctx — a session
// must outlive the tool call that opened it, so exec.CommandContext is
// deliberately not used. Terminate is the only reaper.
func spawnSession(convID string, id int, req dservice.TerminalOpenRequest) (*terminalSession, error) {
	sess := &terminalSession{
		id:          id,
		tty:         req.TTY,
		done:        make(chan struct{}),
		readerEOF:   make(chan struct{}),
		notify:      make(chan struct{}, 1),
		interaction: make(chan struct{}, 1),
		dir:         req.Dir,
	}
	sess.env = sessionEnv()
	sess.touch()
	var err error
	if req.TTY {
		err = sess.spawnPTY(req.Command)
	} else {
		err = sess.spawnPipes(req.Command)
	}
	if err != nil {
		return nil, err
	}
	go sess.wait()
	return sess, nil
}

// spawnPipes runs the command with merged stdout/stderr — assigning the
// same writer to both makes os/exec share one pipe. os/exec's own copy
// goroutine feeds append(), so no reader goroutine is needed here. Stdin
// stays nil — codex's stdin_open=false for non-tty: the child gets
// /dev/null, so a program reading stdin hits EOF instead of blocking on
// a pipe Write would refuse to feed anyway.
func (s *terminalSession) spawnPipes(command string) error {
	cmd := sessionCommand(command)
	cmd.Dir = s.dir
	cmd.Env = s.env
	cmd.WaitDelay = waitDelay
	out := appendWriter{s}
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		return err
	}
	s.cmd = cmd
	return nil
}

// spawnPTY allocates a real pseudo-terminal: the child gets the slave on
// 0/1/2 inside a fresh session with the pty as controlling terminal, so
// isatty probes answer true and the kernel owns job control.
func (s *terminalSession) spawnPTY(command string) error {
	master, slave, err := openPTY()
	if err != nil {
		return err
	}
	cmd, err := ptyCommand(command, slave)
	if err != nil {
		master.Close()
		slave.Close()
		return err
	}
	cmd.Dir = s.dir
	cmd.Env = s.env
	if err := cmd.Start(); err != nil {
		master.Close()
		slave.Close()
		return err
	}
	// The child holds its own slave descriptors — the parent copy must
	// close or master reads never see EOF after the child exits.
	slave.Close()
	s.cmd = cmd
	s.master = master
	go s.readPTY()
	return nil
}

// readPTY drains the master into the pending buffer. The kernel reports
// slave-side exit as EIO — that is EOF, not an error.
func (s *terminalSession) readPTY() {
	buf := make([]byte, 8192)
	for {
		n, err := s.master.Read(buf)
		if n > 0 {
			s.append(buf[:n])
		}
		if err != nil {
			close(s.readerEOF)
			return
		}
	}
}

// wait blocks in cmd.Wait — for the pipes backend that also waits out the
// copy goroutine — then publishes the exit before closing done. Exit code
// is set before done so Collect can never observe Running=false with a
// stale zero.
func (s *terminalSession) wait() {
	err := s.cmd.Wait()
	if err == nil {
		s.exitCode = 0
	} else {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			s.exitCode = exitErr.ExitCode()
		} else {
			s.exitCode = -1
		}
	}
	s.exited.Store(true)
	close(s.done)
	// Pipes path: Wait waited out the copy goroutine, so the stream is
	// already drained — readerEOF may close immediately. The pty path
	// closes it in readPTY on EIO.
	if !s.tty {
		close(s.readerEOF)
	}
}

// appendWriter adapts the session's buffer append to io.Writer — the
// merged stdout/stderr target for the pipes backend. It exists because
// the port's Write([]byte) error for stdin cannot coexist with
// io.Writer's Write([]byte) (int, error) on one type.
type appendWriter struct{ s *terminalSession }

func (w appendWriter) Write(p []byte) (int, error) {
	w.s.append(p)
	return len(p), nil
}

// append drops bytes into pending under the cap — the oldest bytes go and
// the omitted counter keeps the drain honest.
func (s *terminalSession) append(p []byte) {
	s.mu.Lock()
	s.pending = append(s.pending, p...)
	if len(s.pending) > sessionBufferCap {
		drop := len(s.pending) - sessionBufferCap
		s.omitted += drop
		s.pending = append([]byte(nil), s.pending[drop:]...)
	}
	s.mu.Unlock()
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

// drain empties pending — the "chars since last Collect" window — and
// prepends the omission marker when the cap dropped bytes. The returned
// count is the bytes that flowed through this drain (retained + omitted)
// so a Collect can report the honest pre-truncation total.
func (s *terminalSession) drain() (string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	flowed := len(s.pending) + s.omitted
	if len(s.pending) == 0 {
		if s.omitted > 0 {
			marker := fmt.Sprintf("... %d bytes omitted ...\n", s.omitted)
			s.omitted = 0
			return marker, flowed
		}
		return "", 0
	}
	out := string(s.pending)
	s.pending = nil
	if s.omitted > 0 {
		out = fmt.Sprintf("... %d bytes omitted ...\n", s.omitted) + out
		s.omitted = 0
	}
	return out, flowed
}

func (s *terminalSession) ID() int   { return s.id }
func (s *terminalSession) TTY() bool { return s.tty }
func (s *terminalSession) touch()    { s.lastUsed.Store(time.Now().UnixNano()) }

// tryInteraction mirrors codex's try_lock_owned on the interaction lock:
// true means the semaphore was free (acquired then released here), false
// means a write/poll interaction currently holds it — the store's
// eviction uses this to leave in-flight sessions alone.
func (s *terminalSession) tryInteraction() bool {
	select {
	case s.interaction <- struct{}{}:
		<-s.interaction
		return true
	default:
		return false
	}
}

// Exited reports process death with the code published before done
// closed — the pair is always consistent.
func (s *terminalSession) Exited() (bool, int) {
	if !s.exited.Load() {
		return false, 0
	}
	return true, s.exitCode
}

// Write feeds stdin through the pty master — TTY sessions only. Pipes
// sessions report ErrTerminalStdinClosed, matching codex: interactive
// input requires the tty backend, and \x03 goes through Interrupt. A dead
// process reports "session exited"; a live one that ignores stdin may
// block the write until Terminate lands.
func (s *terminalSession) Write(p []byte) error {
	if len(p) == 0 {
		return nil
	}
	if !s.tty {
		return dservice.ErrTerminalStdinClosed
	}
	s.touch()
	if s.exited.Load() {
		return errors.New("session exited")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.master.Write(p)
	return err
}

// Interrupt delivers Ctrl-C. On a pty the byte goes through the line
// discipline — the kernel signals the foreground group; on pipes there
// is no line discipline, so the process group gets SIGINT directly.
func (s *terminalSession) Interrupt() error {
	if s.exited.Load() {
		return errors.New("session exited")
	}
	if s.tty {
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
		_, err := s.master.Write([]byte{3})
		return err
	}
	return killGroup(s.cmd.Process.Pid, interruptSignal)
}

// Collect drains pending and waits up to maxWait for more output or the
// process exit. ctx cancel returns the accumulated output with the
// session left running — interruption never kills background work.
func (s *terminalSession) Collect(ctx context.Context, maxWait time.Duration, sink func(string)) dservice.TerminalPoll {
	s.touch()
	deadline := time.NewTimer(maxWait)
	defer deadline.Stop()
	var out strings.Builder
	var total int
	flush := func() {
		chunk, n := s.drain()
		total += n
		if chunk != "" {
			if sink != nil {
				sink(chunk)
			}
			out.WriteString(chunk)
		}
	}
	for {
		flush()
		if s.exited.Load() {
			// One last window for the reader to drain bytes written just
			// before death — pty output can still be in flight.
			select {
			case <-s.readerEOF:
			case <-time.After(postExitDrainWait):
			case <-ctx.Done():
			}
			flush()
			_, code := s.Exited()
			return dservice.TerminalPoll{Output: out.String(), ExitCode: code, TotalBytes: total}
		}
		select {
		case <-s.notify:
		case <-s.done:
		case <-ctx.Done():
			return s.pollState(out.String(), total)
		case <-deadline.C:
			return s.pollState(out.String(), total)
		}
	}
}

func (s *terminalSession) pollState(out string, total int) dservice.TerminalPoll {
	if exited, code := s.Exited(); exited {
		return dservice.TerminalPoll{Output: out, ExitCode: code, TotalBytes: total}
	}
	return dservice.TerminalPoll{Output: out, Running: true, TotalBytes: total}
}

// Terminate kills the process tree and waits out the goroutines. The
// session stays usable for a final Exited check; Write and Interrupt
// report the exited error.
func (s *terminalSession) Terminate() {
	if !s.terminated.CompareAndSwap(false, true) {
		return
	}
	s.terminateProcess()
	// Close the write side first — a blocked Write unblocks on EPIPE;
	// master close also ends the pty reader loop.
	if s.master != nil {
		s.master.Close()
	}
	<-s.done
	// Reap the reader goroutine; pipes close it in wait() already.
	select {
	case <-s.readerEOF:
	case <-time.After(waitDelay):
	}
}
