// Package job holds the conversation use-cases as executable commands —
// each job is self-contained: Execute carries the whole flow (load state,
// orchestrate domain ports, persist, emit events) and replies on its own
// result channel. The application layer submits them to a worker.Worker;
// the worker.Job contract (Execute, Interrupt, ErrTerminate) is the only
// mailbox knowledge — queue topology stays invisible.
package job

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/jahrulnr/nusallm/infrastructure/config"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
	"github.com/jahrulnr/nusallm/infrastructure/worker"
)

// Deps is everything a job may reach — the domain ports plus the
// conversation config, injected at construction. Jobs never see the app
// or the worker.
type Deps struct {
	Convs        drepo.Conversation
	Gateways     drepo.Gateway
	Environments map[enum.Environment]EnvironmentDriver
	Config       config.ConversationConfig
}

// Base is the shared machinery for room jobs: the bound conversation id,
// the caller's result channel, and interruption as ctx cancellation.
// Embedding it satisfies the worker.Job Interrupt contract plus the
// Reply/Bind plumbing the application consumes.
type Base struct {
	Result chan error // buffered 1 — Reply never blocks

	convID      string
	interrupted atomic.Bool
	cancel      atomic.Value // context.CancelFunc of the live Execute
}

// Bind pins the job to a room — idempotent; the broker calls it at post
// time and again on every repost across workers.
func (j *Base) Bind(convID string) { j.convID = convID }

// ConvID is the room this job runs against.
func (j *Base) ConvID() string { return j.convID }

// Reply delivers the terminal error to whoever submitted the job. A nil
// Result — a hand-built job — is a no-op rather than a blocking send
// that would wedge the worker goroutine forever.
func (j *Base) Reply(err error) {
	if j.Result != nil {
		j.Result <- err
	}
}

// IsPreempt reports whether the job cuts the queue line — ordinary jobs
// inherit false; control jobs (edit, delete) override it. The broker
// reads it at post time, so a job reposted across workers keeps its
// preemption.
func (j *Base) IsPreempt() bool { return false }

// finish is the deferred tail of every Execute: a job panic becomes a
// 500 instead of propagating through the worker, and the caller's result
// channel is always served — a wedged waiter is worse than an error.
// worker.ErrTerminate is a control signal, not an op result — the caller
// gets the success it earned, not the sentinel.
func (j *Base) finish(err *error) {
	if r := recover(); r != nil {
		*err = apperror.New(500, "job failed", fmt.Errorf("panic: %v", r))
	}
	if errors.Is(*err, worker.ErrTerminate) {
		j.Reply(nil)
		return
	}
	j.Reply(*err)
}

// Interrupt cancels the Execute ctx — the default stop for room jobs.
// The flag is sticky and stored before the cancel is loaded, pairing with
// Cancellable's store-then-check so a racing Interrupt can't slip through.
func (j *Base) Interrupt() {
	j.interrupted.Store(true)
	if c, ok := j.cancel.Load().(context.CancelFunc); ok {
		c()
	}
}

// Cancellable derives the execution ctx: it dies on Interrupt, on the
// worker's own ctx (Terminate's hard stop), or on the caller's ctx.
func (j *Base) Cancellable(execCtx, callerCtx context.Context) context.Context {
	if callerCtx == nil {
		callerCtx = context.Background()
	}
	ctx, cancel := context.WithCancel(execCtx)
	j.cancel.Store(cancel)
	context.AfterFunc(callerCtx, cancel)
	if j.interrupted.Load() {
		cancel()
	}
	return ctx
}
