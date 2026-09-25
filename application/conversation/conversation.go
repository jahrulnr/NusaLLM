// Package appconversation is the broker between callers and per-room
// workers: it owns the worker map, submits jobs, and handles worker
// lifecycle (idle eviction, delete teardown). The use-case bodies live in
// application/job — this package only routes work to a room.
//
// Concurrency model: each conversation is owned by one worker.Worker — a
// per-room serial executor. Send queues FIFO; Edit and Delete preempt —
// they interrupt the running job and cut ahead of the wait line, so an
// edit lands on top of the turn it just killed and queued sends still run
// against the rewritten history. Delete retires the worker: queued jobs
// get 410, and submits racing the teardown get ErrClosed and are reposted
// onto the fresh worker the next post spawns — a job that arrives after
// the room died belongs to the next room, not the corpse. Workers are
// reaped when their queue drains, so FE-minted ids can't grow the map.
package appconversation

import (
	"errors"
	"fmt"
	"sync"

	appjob "github.com/jahrulnr/llm-base/application/job"
	"github.com/jahrulnr/llm-base/infrastructure/config"
	"github.com/jahrulnr/llm-base/infrastructure/domain/enum"
	drepo "github.com/jahrulnr/llm-base/infrastructure/domain/repository"
	dservice "github.com/jahrulnr/llm-base/infrastructure/domain/service"
	apperror "github.com/jahrulnr/llm-base/infrastructure/error"
	"github.com/jahrulnr/llm-base/infrastructure/worker"
)

// queuedJob is what a room's worker runs: a worker.Job plus the result
// channel (Reply), room binding (Bind), and preemption intent
// (IsPreempt) the broker attaches.
type queuedJob interface {
	worker.Job
	Reply(err error)
	Bind(convID string)
	IsPreempt() bool
}

// ConversationApp is the broker. cfg is the normalized conversation
// config (mailbox size feeds the workers, turn limits ride inside deps);
// workers maps conversation id → its serial executor.
type ConversationApp struct {
	cfg     config.ConversationConfig
	deps    appjob.Deps
	mu      sync.Mutex
	workers map[string]*worker.Worker
}

func New(
	convs drepo.Conversation,
	gateways drepo.Gateway,
	services map[enum.Style]dservice.TextModelService,
	tools map[string]dservice.Tool,
	cfg config.Config,
) *ConversationApp {
	// Normalize once at the boundary — invalid values clamp and zeros
	// pull their `default` tag, so every downstream read trusts the
	// value.
	ccfg := cfg.Conversation
	ccfg.Normalize()
	return &ConversationApp{
		cfg: ccfg,
		deps: appjob.Deps{
			Convs:    convs,
			Gateways: gateways,
			Services: services,
			Tools:    tools,
			Config:   ccfg,
		},
		workers: map[string]*worker.Worker{},
	}
}

// post enqueues a job on the room's worker, spawning one on demand.
// Preempt jobs (edit, delete) cut the line and interrupt whatever runs;
// ordinary jobs append FIFO. A submit racing the worker's teardown gets
// ErrClosed — respawn and retry, which is also how jobs stranded by a
// delete find the next room.
func (app *ConversationApp) post(convID string, j queuedJob) {
	j.Bind(convID)
	for {
		app.mu.Lock()
		w := app.workers[convID]
		if w == nil || w.Dead() {
			w = worker.New(worker.Options{
				QueueCap: app.cfg.MailboxCap,
				OnIdle:   func(idle *worker.Worker) { app.evict(convID, idle) },
				OnTerminate: func(term *worker.Worker, drained []worker.Job) {
					app.onTerminated(convID, term, drained)
				},
			})
			app.workers[convID] = w
		}
		app.mu.Unlock()

		var err error
		if j.IsPreempt() {
			err = w.Preempt(j)
		} else {
			err = w.Submit(j)
		}
		switch {
		case errors.Is(err, worker.ErrClosed):
			continue
		case errors.Is(err, worker.ErrFull):
			j.Reply(apperror.New(429, "conversation queue full", fmt.Errorf("mailbox capacity %d", app.cfg.MailboxCap)))
		}
		return
	}
}

// evict retires an idle worker — called by the worker's OnIdle hook and
// from paths that discover an idle worker (e.g. Cancel finding nothing
// running). Anything submitted in the race window lands in Terminate's
// drain and is reposted onto the successor worker.
func (app *ConversationApp) evict(convID string, w *worker.Worker) {
	app.mu.Lock()
	if app.workers[convID] != w || !w.Idle() {
		app.mu.Unlock()
		return
	}
	drained := w.Terminate()
	delete(app.workers, convID)
	app.mu.Unlock()
	for _, j := range drained {
		if aj, ok := j.(queuedJob); ok {
			app.post(convID, aj) // IsPreempt rides on the job, not the call site
		}
	}
}

// onTerminated runs when a worker self-terminates (a delete job retired
// it): queued jobs die with the room — 410 Gone — and the map entry goes,
// so the next post spawns a fresh worker.
func (app *ConversationApp) onTerminated(convID string, w *worker.Worker, drained []worker.Job) {
	gone := apperror.New(410, "conversation deleted", fmt.Errorf("room %s removed", convID))
	for _, j := range drained {
		if aj, ok := j.(queuedJob); ok {
			aj.Reply(gone)
		}
	}
	app.mu.Lock()
	if app.workers[convID] == w {
		delete(app.workers, convID)
	}
	app.mu.Unlock()
}
