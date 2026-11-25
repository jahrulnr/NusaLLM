// Package worker provides a serial executor for interruptible jobs: one
// goroutine drains a bounded queue and runs a single Job at a time, so
// shared state behind the worker stays single-writer by construction.
//
// It is the only implementation of the job-executor port declared in
// infrastructure/domain/service; the application takes that port and the
// composition root (cmd) hands this constructor to it. The contract types
// (Job, Steerable, WorkerOptions, the sentinels) are aliases of the port —
// one declaration, one ErrTerminate value, so errors.Is holds on both sides
// of the seam.
package worker

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"sync"

	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
)

// Job is the port's unit of scheduled work.
type Job = dservice.Job

// Steerable is the port's optional mid-run input contract.
type Steerable = dservice.Steerable

// Options tunes a Worker; the port owns the fields and their meaning.
type Options = dservice.WorkerOptions

var (
	// ErrClosed rejects Submit/Preempt on a terminated worker — the
	// caller respawns or re-routes.
	ErrClosed = dservice.ErrClosed
	// ErrFull rejects Submit when the queue is at capacity.
	ErrFull = dservice.ErrFull
	// ErrNotRunning answers Interrupt when nothing is running.
	ErrNotRunning = dservice.ErrNotRunning
	// ErrTerminate is the Execute return that retires the worker — the
	// job finishes, the queue drains to the OnTerminate hook, and the
	// loop exits.
	ErrTerminate = dservice.ErrTerminate
)

const defaultQueueCap = 128

// Worker is a serial job executor. Constructed workers start consuming
// immediately; Terminate or a job returning ErrTerminate stops the loop.
type Worker struct {
	opts     Options
	doorbell chan struct{}
	done     chan struct{}

	mu      sync.Mutex
	queue   []Job
	running Job
	runKill context.CancelFunc
	runGID  uint64 // goroutine executing the running job — 0 when idle
	unwind  bool   // the running job was asked to stop — refuse new steer
	dead    bool
}

// New spawns a worker whose loop is live before New returns.
func New(opts Options) *Worker {
	if opts.QueueCap <= 0 {
		opts.QueueCap = defaultQueueCap
	}
	w := &Worker{
		opts:     opts,
		doorbell: make(chan struct{}, 1),
		done:     make(chan struct{}),
	}
	go w.loop()
	return w
}

// Submit queues a job FIFO. ErrFull when the queue is at capacity,
// ErrClosed once the worker is dead.
func (w *Worker) Submit(j Job) error {
	w.mu.Lock()
	switch {
	case w.dead:
		w.mu.Unlock()
		return ErrClosed
	case len(w.queue) >= w.opts.QueueCap:
		w.mu.Unlock()
		return ErrFull
	}
	w.queue = append(w.queue, j)
	w.mu.Unlock()
	w.signal()
	return nil
}

// Preempt queues a job ahead of everything waiting and interrupts the
// running job — the job that "couldn't wait" runs next. Queue capacity
// does not apply: control traffic is rare by contract. Two preempts in
// flight run newest-first — the latest control intent wins.
func (w *Worker) Preempt(j Job) error {
	w.mu.Lock()
	if w.dead {
		w.mu.Unlock()
		return ErrClosed
	}
	w.queue = append([]Job{j}, w.queue...)
	running := w.running
	if running != nil {
		w.unwind = true
	}
	w.mu.Unlock()
	w.signal()
	if running != nil {
		running.Interrupt()
	}
	return nil
}

// Steer delivers mid-run input to the running job. False means nothing
// is running, the job takes no input, or the job is already unwinding —
// the caller decides what the undelivered text becomes. Delivery only
// buffers; the job folds input at its own safe point, so Steer never
// races the job's execution. Input handed to an unwinding job would be
// lost when it exits — the worker refuses instead.
func (w *Worker) Steer(text string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.unwind || w.dead {
		return false
	}
	// Buffer under the lock: an Interrupt landing between the check and
	// the push would otherwise take input the job will never fold.
	if s, ok := w.running.(Steerable); ok {
		s.Steer(text)
		return true
	}
	return false
}

// RecallSteer withdraws the newest un-fired input from the running job.
// ok=false — nothing pending (or already consumed, or nothing running).
func (w *Worker) RecallSteer() (string, bool) {
	w.mu.Lock()
	running := w.running
	w.mu.Unlock()
	if s, ok := running.(Steerable); ok {
		return s.RecallSteer()
	}
	return "", false
}

// OnWorkerGoroutine reports whether the CALLER is the goroutine currently
// executing this worker's job. A job's own event handler runs on that
// goroutine — a synchronous same-worker post from inside it would queue
// behind the very job that hosts the handler and deadlock until the
// caller's ctx dies. Callers refuse such posts instead.
func (w *Worker) OnWorkerGoroutine() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.running != nil && w.runGID != 0 && w.runGID == goroutineID()
}

// Interrupt asks the running job to stop early — the job's own hook runs.
// ErrNotRunning when idle, nil when the signal was delivered (the job may
// still be unwinding). The unwinding marker refuses new steer input — a
// job asked to stop will not fold it.
func (w *Worker) Interrupt() error {
	w.mu.Lock()
	running := w.running
	if running != nil {
		w.unwind = true
	}
	w.mu.Unlock()
	if running == nil {
		return ErrNotRunning
	}
	running.Interrupt()
	return nil
}

// Terminate is the hard stop: marks the worker dead, interrupts the
// running job AND kills its context, then returns the drained queue so
// the caller decides what unstarted jobs deserve. Idempotent — a second
// call returns nil. The loop exits as soon as the running job unwinds.
func (w *Worker) Terminate() []Job {
	w.mu.Lock()
	if w.dead {
		w.mu.Unlock()
		return nil
	}
	w.dead = true
	drained := w.queue
	w.queue = nil
	running := w.running
	kill := w.runKill
	w.mu.Unlock()
	if running != nil {
		running.Interrupt()
	}
	if kill != nil {
		kill()
	}
	w.signal()
	return drained
}

// Dead reports whether the worker has been terminated — by Terminate or
// by a job returning ErrTerminate.
func (w *Worker) Dead() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.dead
}

// Idle reports whether nothing is running and the queue is empty.
func (w *Worker) Idle() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return !w.dead && w.running == nil && len(w.queue) == 0
}

// Done closes when the loop exits — after the OnTerminate hook ran —
// so waiters observe a fully retired worker, not a dying one.
func (w *Worker) Done() <-chan struct{} { return w.done }

func (w *Worker) signal() {
	select {
	case w.doorbell <- struct{}{}:
	default:
	}
}

// safeCall runs a lifecycle hook — a hook panic must not kill the loop
// goroutine, or every job queued behind it is orphaned.
func safeCall(f func()) {
	defer func() { _ = recover() }()
	f()
}

// loop is the executor: wake on the doorbell, drain the queue serially.
// Dead workers flush whatever is left to OnTerminate and exit; idle ones
// announce themselves and go back to sleep.
func (w *Worker) loop() {
	for range w.doorbell {
		for {
			w.mu.Lock()
			if w.dead || len(w.queue) == 0 {
				dead := w.dead
				var drained []Job
				if dead {
					drained = w.queue
					w.queue = nil
				}
				w.mu.Unlock()
				if dead {
					if w.opts.OnTerminate != nil {
						safeCall(func() { w.opts.OnTerminate(w, drained) })
					}
					close(w.done)
					return
				}
				if w.opts.OnIdle != nil {
					safeCall(func() { w.opts.OnIdle(w) })
				}
				break
			}
			j := w.queue[0]
			w.queue = w.queue[1:]
			w.running = j
			w.runGID = goroutineID()
			w.unwind = false
			ctx, kill := context.WithCancel(context.Background())
			w.runKill = kill
			w.mu.Unlock()

			// A panicking job must not wedge the worker — running would
			// stay set forever, done would never close, and the queue
			// would fill to a permanent ErrFull. The panic becomes an
			// error return; the job's own result channel still delivers.
			err := func() (err error) {
				defer func() {
					if r := recover(); r != nil {
						err = fmt.Errorf("job panicked: %v", r)
					}
				}()
				return j.Execute(ctx)
			}()

			kill()
			w.mu.Lock()
			w.running = nil
			w.runKill = nil
			w.runGID = 0
			w.unwind = false
			if errors.Is(err, ErrTerminate) {
				w.dead = true
			}
			w.mu.Unlock()
		}
	}
}

// goroutineID reads the calling goroutine's id from its own stack header
// ("goroutine N […]:"). The worker stamps the executor goroutine at
// dequeue so OnWorkerGoroutine can refuse a reentrant post that would
// queue behind the very job running the caller. 0 on a malformed header.
func goroutineID() uint64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	fields := strings.Fields(string(buf[:n]))
	if len(fields) < 2 || fields[0] != "goroutine" {
		return 0
	}
	id, _ := strconv.ParseUint(fields[1], 10, 64)
	return id
}
