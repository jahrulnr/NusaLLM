package service

import (
	"context"
	"errors"
)

// A job executor runs one room's work serially: it drains a bounded queue,
// executes a single Job at a time, and lets control jobs cut the line. The
// split between interruption and termination is deliberate — Interrupt is a
// soft request the running Job's own hook decides how to honor, while
// Terminate is the hard stop that kills the execution context, drains the
// queue, and hands unstarted jobs back to the caller.
//
// Only the mechanism lives here. Which job preempts, what a drained job
// becomes, how large a room's queue may grow, and when a room retires are
// application decisions; they arrive as WorkerOptions and job behaviour.
//
// The types are shared rather than re-declared per layer on purpose: Go
// method identity compares parameter types by name, so an implementation
// can only satisfy an interface whose methods name these exact types, and
// a control signal (ErrTerminate) is only one signal if both sides hold the
// same value.

// Job is a unit of scheduled work. Execute runs on the executor's own
// goroutine and must converge — promptly after Interrupt, and always after
// its ctx dies (Terminate cancels it). Results travel on channels the job
// owns; Execute's return is a control signal — returning ErrTerminate
// retires the executor after the job completes.
//
// Interrupt may be called from any goroutine, possibly more than once, and
// must not block. A Job that runs longer than a moment should give its ctx
// a cancel path inside Interrupt.
type Job interface {
	Execute(ctx context.Context) error
	Interrupt()
}

// Steerable is the optional mid-run input contract a Job may implement. The
// executor delivers input out of band — it never interrupts Execute to
// deliver, so the job folds input at its own safe point. RecallSteer
// withdraws the newest un-fired input so the caller can take it back before
// it lands; ok=false means nothing was pending.
//
// Steer runs while the executor holds its state lock so the
// refuse-or-buffer decision is atomic against Interrupt: it must be a
// leaf-lock, non-blocking buffer append — never I/O, never a call back into
// the executor, the application, or the job's own execution state.
//
// Detection is structural: a job becomes steerable by having these two
// methods, not by declaring the interface.
type Steerable interface {
	Steer(text string)
	RecallSteer() (text string, ok bool)
}

// Retirable is what a lifecycle hook receives: the executor that went idle
// or is terminating. Retiring needs exactly two answers — is it still idle,
// and what is left in its queue — so the hook surface stays this small.
type Retirable interface {
	Idle() bool
	Terminate() []Job
}

// WorkerOptions tunes an executor.
type WorkerOptions struct {
	// QueueCap bounds queued jobs; Submit beyond it returns ErrFull.
	// Preempt bypasses the cap — control jobs are few by design.
	//
	// This is a scheduling mechanism, not conversation policy: the
	// application decides how many jobs a room may queue and passes the
	// number down here.
	QueueCap int
	// OnIdle runs (off-lock) whenever the queue drains with nothing
	// running — the hook for idle reaping. The caller re-checks Idle()
	// before retiring the executor.
	OnIdle func(Retirable)
	// OnTerminate runs after the loop exits, receiving the drained queue —
	// the caller decides their fate.
	OnTerminate func(Retirable, []Job)
}

var (
	// ErrClosed rejects Submit/Preempt on a terminated executor — the
	// caller respawns or re-routes.
	ErrClosed = errors.New("worker closed")
	// ErrFull rejects Submit when the queue is at capacity.
	ErrFull = errors.New("worker queue full")
	// ErrNotRunning answers Interrupt when nothing is running.
	ErrNotRunning = errors.New("no job running")
	// ErrTerminate is the Execute return that retires the executor — the
	// job finishes, the queue drains to the OnTerminate hook, and the loop
	// exits.
	ErrTerminate = errors.New("terminate worker")
)
