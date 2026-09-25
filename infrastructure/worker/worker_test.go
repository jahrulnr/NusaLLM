package worker

import (
	"context"
	"errors"
	"testing"
	"time"
)

// seqJob appends its id to order — FIFO verification.
type seqJob struct {
	id    int
	order *[]int
	done  chan struct{}
}

func (j *seqJob) Execute(context.Context) error {
	if j.order != nil {
		*j.order = append(*j.order, j.id)
	}
	if j.done != nil {
		close(j.done)
	}
	return nil
}
func (j *seqJob) Interrupt() {}

// blockingJob runs until interrupted or the worker ctx dies — the
// "stuck tool" shape.
type blockingJob struct {
	started     chan struct{}
	interrupted chan struct{}
	err         error
}

func (j *blockingJob) Execute(ctx context.Context) error {
	close(j.started)
	select {
	case <-j.interrupted:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (j *blockingJob) Interrupt() { close(j.interrupted) }

func waitClosed(t *testing.T, c <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// Jobs run serially in submit order — the core guarantee.
func TestSerialFIFO(t *testing.T) {
	w := New(Options{})
	defer w.Terminate()
	var order []int
	done := make(chan struct{})
	for i := 0; i < 3; i++ {
		if err := w.Submit(&seqJob{id: i, order: &order}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Submit(&seqJob{id: 3, order: &order, done: done}); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, done, "last job")
	if len(order) != 4 || order[0] != 0 || order[3] != 3 {
		t.Fatalf("execution order wrong: %v", order)
	}
}

// Interrupt reaches the running job's own hook — the worker delivers
// the signal; the job decides how to stop.
func TestInterruptRunning(t *testing.T) {
	w := New(Options{})
	defer w.Terminate()
	j := &blockingJob{started: make(chan struct{}), interrupted: make(chan struct{})}
	if err := w.Submit(j); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, j.started, "job start")
	if err := w.Interrupt(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !w.Idle() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !w.Idle() {
		t.Fatal("interrupted job never unwound")
	}
}

func TestInterruptIdle(t *testing.T) {
	w := New(Options{})
	defer w.Terminate()
	if err := w.Interrupt(); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("idle interrupt → %v, want ErrNotRunning", err)
	}
}

// Preempt interrupts the running job and cuts ahead of queued work.
func TestPreemptCutsLine(t *testing.T) {
	w := New(Options{})
	defer w.Terminate()
	blocked := &blockingJob{started: make(chan struct{}), interrupted: make(chan struct{})}
	if err := w.Submit(blocked); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, blocked.started, "blocking job")

	var order []int
	done := make(chan struct{})
	w.Submit(&seqJob{id: 1, order: &order})
	w.Submit(&seqJob{id: 2, order: &order})
	if err := w.Preempt(&seqJob{id: 9, order: &order}); err != nil {
		t.Fatal(err)
	}
	w.Submit(&seqJob{id: 3, order: &order, done: done})
	waitClosed(t, done, "queue drain")
	// The preempted job runs first; queued FIFO resumes after.
	if len(order) != 4 || order[0] != 9 || order[1] != 1 || order[2] != 2 || order[3] != 3 {
		t.Fatalf("preempt order wrong: %v", order)
	}
	select {
	case <-blocked.interrupted:
	default:
		t.Fatal("preempt did not interrupt the running job")
	}
}

// Terminate drains the queue to the caller and rejects further submits.
func TestTerminateDrains(t *testing.T) {
	w := New(Options{})
	blocked := &blockingJob{started: make(chan struct{}), interrupted: make(chan struct{})}
	w.Submit(blocked)
	waitClosed(t, blocked.started, "blocking job")
	w.Submit(&seqJob{id: 1})
	w.Submit(&seqJob{id: 2})
	drained := w.Terminate()
	if len(drained) != 2 {
		t.Fatalf("drained %d jobs, want 2", len(drained))
	}
	waitClosed(t, w.Done(), "worker exit")
	if err := w.Submit(&seqJob{id: 3}); !errors.Is(err, ErrClosed) {
		t.Fatalf("submit after terminate → %v, want ErrClosed", err)
	}
}

// A job returning ErrTerminate retires the worker; the queue drains to
// the OnTerminate hook.
func TestJobSelfTerminate(t *testing.T) {
	drainedC := make(chan []Job, 1)
	w := New(Options{OnTerminate: func(_ *Worker, drained []Job) { drainedC <- drained }})
	if err := w.Submit(&terminateJob{}); err != nil {
		t.Fatal(err)
	}
	w.Submit(&seqJob{id: 7})
	w.Submit(&seqJob{id: 8})
	var drained []Job
	select {
	case drained = <-drainedC:
	case <-time.After(2 * time.Second):
		t.Fatal("OnTerminate never fired")
	}
	if len(drained) != 2 {
		t.Fatalf("OnTerminate drained %d, want 2", len(drained))
	}
	waitClosed(t, w.Done(), "worker exit")
}

type terminateJob struct{}

func (terminateJob) Execute(context.Context) error { return ErrTerminate }
func (terminateJob) Interrupt()                    {}

// OnIdle fires when the queue drains — the reaping hook.
func TestOnIdleFires(t *testing.T) {
	idle := make(chan *Worker, 4)
	w := New(Options{OnIdle: func(w *Worker) { idle <- w }})
	defer w.Terminate()
	done := make(chan struct{})
	w.Submit(&seqJob{id: 1, done: done})
	waitClosed(t, done, "job")
	select {
	case <-idle:
	case <-time.After(2 * time.Second):
		t.Fatal("OnIdle never fired")
	}
}

// Queue capacity bounds Submit — ErrFull, while Preempt stays unlimited.
func TestQueueFull(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	w := New(Options{QueueCap: 1})
	defer w.Terminate()
	w.Submit(&blockingJob{started: make(chan struct{}), interrupted: make(chan struct{})})
	w.Submit(&seqJob{id: 1}) // fills the queue
	if err := w.Submit(&seqJob{id: 2}); !errors.Is(err, ErrFull) {
		t.Fatalf("overflow → %v, want ErrFull", err)
	}
	if err := w.Preempt(&seqJob{id: 9}); err != nil {
		t.Fatalf("control jobs bypass the cap: %v", err)
	}
}
