package appconversation

import (
	"context"
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/config"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
)

// fakeWorker records what the broker asks of a room executor.
type fakeWorker struct {
	submitted []dservice.Job
	preempted []dservice.Job
	done      chan struct{}
}

func (f *fakeWorker) Submit(job dservice.Job) error {
	f.submitted = append(f.submitted, job)
	return nil
}
func (f *fakeWorker) Preempt(job dservice.Job) error {
	f.preempted = append(f.preempted, job)
	return nil
}
func (f *fakeWorker) Interrupt() error            { return dservice.ErrNotRunning }
func (f *fakeWorker) Terminate() []dservice.Job   { return nil }
func (f *fakeWorker) Steer(string) bool           { return false }
func (f *fakeWorker) RecallSteer() (string, bool) { return "", false }
func (f *fakeWorker) Idle() bool                  { return true }
func (f *fakeWorker) Dead() bool                  { return false }
func (f *fakeWorker) Done() <-chan struct{}       { return f.done }
func (f *fakeWorker) OnWorkerGoroutine() bool     { return false }

// fakeJob is the broker-side contract only.
type fakeJob struct {
	reply chan error
	bound string
}

func (j *fakeJob) Execute(context.Context) error { return nil }
func (j *fakeJob) Interrupt()                    {}
func (j *fakeJob) Reply(err error)               { j.reply <- err }
func (j *fakeJob) Bind(convID string)            { j.bound = convID }
func (j *fakeJob) IsPreempt() bool               { return false }

// The broker builds rooms through the injected factory, sizes their queue
// from the conversation config, binds the job, and hands back the executor
// that accepted it — the whole reason the port exists.
func TestPostBuildsRoomThroughInjectedFactory(t *testing.T) {
	room := &fakeWorker{done: make(chan struct{})}
	var seen dservice.WorkerOptions
	calls := 0
	factory := func(opts dservice.WorkerOptions) Worker {
		calls++
		seen = opts
		return room
	}
	app := New(nil, nil, nil, config.NewConfig(), nil, "", nil, nil, factory)

	j := &fakeJob{reply: make(chan error, 1)}
	if got := app.post("room", j); got != room {
		t.Fatalf("post returned %v, want the factory's executor", got)
	}
	if calls != 1 {
		t.Fatalf("factory calls = %d, want 1 for the first post", calls)
	}
	if seen.QueueCap != app.cfg.MailboxCap {
		t.Fatalf("QueueCap = %d, want the configured mailbox cap %d", seen.QueueCap, app.cfg.MailboxCap)
	}
	if seen.OnIdle == nil || seen.OnTerminate == nil {
		t.Fatal("lifecycle hooks must be wired for every room")
	}
	if len(room.submitted) != 1 || j.bound != "room" {
		t.Fatalf("submitted = %d bound = %q, want the job queued on its room", len(room.submitted), j.bound)
	}

	// A live executor is reused — the factory is a spawn path, not a
	// per-post cost.
	second := &fakeJob{reply: make(chan error, 1)}
	app.post("room", second)
	if calls != 1 || len(room.submitted) != 2 {
		t.Fatalf("factory calls = %d submitted = %d, want the live room reused", calls, len(room.submitted))
	}
}

// Without a factory the broker cannot serve a room at all: failing loudly at
// composition beats a nil dereference on the first send.
func TestNewRejectsMissingWorkerFactory(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("New accepted a nil worker factory")
		}
	}()
	New(nil, nil, nil, config.NewConfig(), nil, "", nil, nil, nil)
}
