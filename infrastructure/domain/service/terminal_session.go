package service

import (
	"context"
	"time"
)

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
	Get(convID string, id int) (TerminalSession, bool)
	Terminate(convID string, id int) bool
	TerminateConversation(convID string)
	TerminateAll()
}

// TerminalSession is one running child process. Write feeds stdin; Collect
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
// prompts, REPLs); the default pipes backend is preferred for everything
// else because it needs no terminal emulation.
type TerminalOpenRequest struct {
	Command string
	Dir     string
	TTY     bool
}

// TerminalPoll is one Collect result: the output drained since the previous
// Collect plus the process state at return. Output already carries an
// "... N bytes omitted ..." marker when the bounded buffer dropped bytes.
type TerminalPoll struct {
	Output   string
	Running  bool
	ExitCode int
}
