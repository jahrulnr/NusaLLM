package appconversation

import (
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
)

// Worker is the room executor as this package drives it: one serial
// executor per conversation, exposing only the surface the broker actually
// calls — submit, preempt, interrupt, terminate, steer, and the status and
// lifecycle hooks that decide when a room is reaped.
//
// The port is declared by its consumer so the broker names no concrete
// scheduler: infrastructure/worker satisfies it structurally, and the
// composition root (cmd) supplies the factory. The contract *types* it
// mentions — Job, Steerable, WorkerOptions, and the sentinels — come from
// infrastructure/domain/service, because Go compares method signatures by
// parameter type and because a control signal such as ErrTerminate is only
// one signal if both sides hold the same value.
type Worker interface {
	Submit(job dservice.Job) error
	Preempt(job dservice.Job) error
	Interrupt() error
	Terminate() []dservice.Job
	Steer(text string) bool
	RecallSteer() (text string, ok bool)
	Idle() bool
	Dead() bool
	Done() <-chan struct{}
	OnWorkerGoroutine() bool
}

// WorkerFactory builds the executor that serves one room. The broker calls
// it when a room has no live executor — a first post, or a repost after a
// retired one.
type WorkerFactory func(dservice.WorkerOptions) Worker

// Workers turns a concrete executor constructor into the factory the broker
// takes, so the composition root stays one line and no call site repeats the
// adapter. T is inferred; the constraint is what proves the concrete worker
// implements every method the broker calls.
func Workers[T Worker](mk func(dservice.WorkerOptions) T) WorkerFactory {
	return func(opts dservice.WorkerOptions) Worker { return mk(opts) }
}
