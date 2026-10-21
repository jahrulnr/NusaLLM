package service

import (
	"context"
	"errors"
	"time"
)

// ErrTerminalSessionGone reports an interaction against a session id that
// no longer resolves — the session was reaped, evicted, or its id reused
// while the call queued on the session's interaction lock.
var ErrTerminalSessionGone = errors.New("terminal session gone")

// ErrTerminalStdinClosed reports a Write against a session spawned without
// TTY — a pipes child gets a closed stdin (immediate EOF), so only
// Interrupt is accepted. Interactive input requires TerminalOpenRequest.TTY.
var ErrTerminalStdinClosed = errors.New("terminal session stdin is closed")

// TerminalSessions owns the long-lived child processes tools open on behalf
// of a conversation — one registry per process, conversation-keyed, shared
// across rooms. Unlike CommandRunner's one-shot Run, sessions outlive the
// tool call that created them: ctx on Open covers the spawn only, never the
// session lifetime. Implementations reap through TerminateConversation when
// a room dies and TerminateAll on shutdown — sessions must never die with an
// arbitrary job ctx, or an interrupted turn would kill the background work
// the model just launched.
type TerminalSessions interface {
	Open(ctx context.Context, convID string, req TerminalOpenRequest) (TerminalSession, error)
	// Interact runs fn while holding the session's interaction lock —
	// calls against one session serialize because they share the draining
	// output buffer and stdin. After the lock lands, the registry is
	// re-checked: if the id was reaped or reused while the call queued,
	// fn never runs and ErrTerminalSessionGone is returned. A ctx that
	// dies while queued returns ctx.Err.
	Interact(ctx context.Context, convID string, id int, fn func(TerminalSession) error) error
	Terminate(convID string, id int) bool
	TerminateConversation(convID string)
	TerminateAll()
}

// TerminalSession is one running child process. Write feeds stdin — TTY
// sessions only, pipes sessions report ErrTerminalStdinClosed; Collect
// drains the buffered output since the previous Collect and waits up to
// maxWait for more — an empty drain is a poll, so there is no separate read
// verb. Interrupt delivers a Ctrl-C (SIGINT to the child's process group).
// Terminate SIGKILLs the group and reaps the session's goroutines; after it
// returns, no session resource may leak.
type TerminalSession interface {
	ID() int
	TTY() bool
	Write(p []byte) error
	Interrupt() error
	Collect(ctx context.Context, maxWait time.Duration, sink func(chunk string)) TerminalPoll
	Exited() (exit bool, code int)
	Terminate()
}

// TerminalOpenRequest is the spawn contract. TTY asks for a real
// pseudo-terminal — required by programs that probe isatty (password
// prompts, REPLs) and the only backend that keeps stdin open for Write;
// the default pipes backend is preferred for everything else because it
// needs no terminal emulation, and its closed stdin gives readers a
// deterministic EOF instead of blocking on input that can never arrive.
type TerminalOpenRequest struct {
	Command string
	Dir     string
	TTY     bool
}

// TerminalPoll is one Collect result: the output drained since the previous
// Collect plus the process state at return. Output already carries an
// "... N bytes omitted ..." marker when the bounded buffer dropped bytes.
// TotalBytes counts everything the process emitted in the drained window —
// retained plus omitted — so callers report the honest pre-truncation
// scale even after the cap dropped head bytes.
type TerminalPoll struct {
	Output     string
	Running    bool
	ExitCode   int
	TotalBytes int
}
