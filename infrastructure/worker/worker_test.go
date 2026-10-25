package worker

import (
	"context"
	"errors"
	"sync"
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

// steerableJob is a blocking job with the optional mid-run input
// contract — the worker delivers Steer out of band and never interrupts
// Execute for it.
type steerableJob struct {
	started     chan struct{}
	interrupted chan struct{}
	mu          sync.Mutex
	steers      []string
}

func (j *steerableJob) Execute(ctx context.Context) error {
	close(j.started)
	select {
	case <-j.interrupted:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (j *steerableJob) Interrupt() { close(j.interrupted) }
func (j *steerableJob) Steer(text string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.steers = append(j.steers, text)
}
func (j *steerableJob) RecallSteer() (string, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if len(j.steers) == 0 {
		return "", false
	}
	last := j.steers[len(j.steers)-1]
	j.steers = j.steers[:len(j.steers)-1]
	return last, true
}
func (j *steerableJob) steerCount() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return len(j.steers)
}

// Steer buffers input on the running job — delivery is confirmed by the
// return value; the job folds it at its own safe point.
func TestSteerDeliveredToRunningJob(t *testing.T) {
	w := New(Options{})
	defer w.Terminate()
	j := &steerableJob{started: make(chan struct{}), interrupted: make(chan struct{})}
	if err := w.Submit(j); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, j.started, "steerable job")
	if !w.Steer("hold on") || !w.Steer("and this") {
		t.Fatal("steer to a running steerable job must deliver")
	}
	if j.steerCount() != 2 {
		t.Fatalf("job buffered %d steers, want 2", j.steerCount())
	}
	if got, ok := w.RecallSteer(); !ok || got != "and this" {
		t.Fatalf("recall returns the newest pending input: %q,%v", got, ok)
	}
	if got, ok := w.RecallSteer(); !ok || got != "hold on" {
		t.Fatalf("recall drains LIFO: %q,%v", got, ok)
	}
	if _, ok := w.RecallSteer(); ok {
		t.Fatal("recall on an empty inbox must report nothing")
	}
}

// An idle worker and a job without the contract both refuse delivery —
// the caller decides what the undelivered text becomes.
func TestSteerRefused(t *testing.T) {
	w := New(Options{})
	defer w.Terminate()
	if w.Steer("nothing running") {
		t.Fatal("steer on an idle worker must refuse")
	}
	if _, ok := w.RecallSteer(); ok {
		t.Fatal("recall on an idle worker must report nothing")
	}
	j := &blockingJob{started: make(chan struct{}), interrupted: make(chan struct{})}
	if err := w.Submit(j); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, j.started, "non-steerable job")
	if w.Steer("no inbox") {
		t.Fatal("steer to a job without the contract must refuse")
	}
	if _, ok := w.RecallSteer(); ok {
		t.Fatal("recall from a job without the contract must report nothing")
	}
}

// doomedSteerJob unwinds through a release gate after Interrupt — a
// window where the worker still reports it running but it will never
// reach another safe point.
type doomedSteerJob struct {
	started       chan struct{}
	interrupted   chan struct{}
	unwinding     chan struct{}
	releaseExit   chan struct{}
	interruptOnce sync.Once
	mu            sync.Mutex
	steers        []string
}

func (j *doomedSteerJob) Execute(context.Context) error {
	close(j.started)
	<-j.interrupted
	close(j.unwinding)
	<-j.releaseExit // still the worker's running job while it unwinds
	return nil
}
func (j *doomedSteerJob) Interrupt() {
	j.interruptOnce.Do(func() { close(j.interrupted) })
}
func (j *doomedSteerJob) Steer(text string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.steers = append(j.steers, text)
}
func (j *doomedSteerJob) RecallSteer() (string, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if len(j.steers) == 0 {
		return "", false
	}
	last := j.steers[len(j.steers)-1]
	j.steers = j.steers[:len(j.steers)-1]
	return last, true
}
func (j *doomedSteerJob) buffered() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return len(j.steers)
}

// A steer landing on a job that is already unwinding must be refused —
// the job exits without ever consuming it and, once the running slot
// clears, the input is unreachable: RecallSteer can no longer return it
// to the caller. Interrupt, Preempt, and Terminate all mark the running
// job unwinding so Steer keeps the text in the caller's hands.
func TestSteerToUnwindingJobIsLost(t *testing.T) {
	w := New(Options{})
	defer w.Terminate()
	j := &doomedSteerJob{
		started:     make(chan struct{}),
		interrupted: make(chan struct{}),
		unwinding:   make(chan struct{}),
		releaseExit: make(chan struct{}),
	}
	if err := w.Submit(j); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, j.started, "job start")
	if err := w.Interrupt(); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, j.unwinding, "job unwind")
	// The job is interrupted and mid-unwind — w.running has not been
	// cleared yet, but Steer must already refuse.
	if w.Steer("too late") {
		t.Fatal("steer to an unwinding job reported delivered — the input would exit with the job unconsumed and unrecoverable")
	}
	close(j.releaseExit)
	deadline := time.Now().Add(2 * time.Second)
	for !w.Idle() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if j.buffered() != 0 {
		t.Fatalf("refused steer still buffered on the doomed job: %d", j.buffered())
	}
	if got, ok := w.RecallSteer(); ok {
		t.Fatalf("RecallSteer returned %q after the job exited — nothing should be recoverable", got)
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
