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
// Shutdown rejects further posts, terminates every worker, and waits for
// their loops to exit without closing the repository.
package appconversation

import (
	"errors"
	"fmt"
	"sync"

	appjob "github.com/jahrulnr/nusallm/application/job"
	"github.com/jahrulnr/nusallm/infrastructure/config"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
	"github.com/jahrulnr/nusallm/infrastructure/worker"
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
	cfg        config.ConversationConfig
	configPath string
	deps       appjob.Deps
	mu         sync.Mutex
	workers    map[string]*worker.Worker

	shuttingDown bool
	shutdownDone chan struct{}
	workerWG     sync.WaitGroup
}

func New(
	convs drepo.Conversation,
	gateways drepo.Gateway,
	services map[enum.Style]dservice.TextModelService,
	tools map[string]dservice.Tool,
	cfg config.Config,
	configPath string,
) *ConversationApp {
	// Normalize once at the boundary — invalid values clamp and zeros
	// pull their `default` tag, so every downstream read trusts the
	// value.
	ccfg := cfg.Conversation
	ccfg.Normalize()
	return &ConversationApp{
		cfg:        ccfg,
		configPath: configPath,
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
// ordinary jobs append FIFO. Posting stays under app.mu through Submit so
// Shutdown has a clear boundary between accepted and rejected work. A
// submit racing a worker's teardown gets ErrClosed — respawn and retry.
func (app *ConversationApp) reloadConfigLocked() {
	if app.configPath == "" {
		return
	}
	var fresh config.Config
	if err := fresh.Load(app.configPath); err != nil {
		return
	}
	ccfg := fresh.Conversation
	ccfg.Normalize()
	app.cfg = ccfg
	app.deps.Config = ccfg
}

// depsSnapshot returns a shallow copy of deps under app.mu so entry points
// do not read app.deps concurrently with reloadConfigLocked.
func (app *ConversationApp) depsSnapshot() appjob.Deps {
	app.mu.Lock()
	defer app.mu.Unlock()
	return app.deps
}

func setJobDeps(j queuedJob, deps appjob.Deps) {
	switch x := j.(type) {
	case *appjob.Send:
		x.Deps = deps
	case *appjob.Edit:
		x.Deps = deps
	case *appjob.Delete:
		x.Deps = deps
	case *appjob.Compact:
		x.Deps = deps
	case *appjob.Rename:
		x.Deps = deps
	}
}

func (app *ConversationApp) post(convID string, j queuedJob) {
	for {
		app.mu.Lock()
		app.reloadConfigLocked()
		deps := app.deps
		cfg := app.cfg
		setJobDeps(j, deps)
		j.Bind(convID)
		if app.shuttingDown {
			app.mu.Unlock()
			j.Reply(apperror.New(503, "conversation application is shutting down", nil))
			return
		}
		w := app.workers[convID]
		if w == nil || w.Dead() {
			w = worker.New(worker.Options{
				QueueCap: cfg.MailboxCap,
				OnIdle:   func(idle *worker.Worker) { app.evict(convID, idle) },
				OnTerminate: func(term *worker.Worker, drained []worker.Job) {
					app.onTerminated(convID, term, drained)
				},
			})
			app.workerWG.Add(1)
			go func(w *worker.Worker) {
				<-w.Done()
				app.workerWG.Done()
			}(w)
			app.workers[convID] = w
		}

		var err error
		if j.IsPreempt() {
			err = w.Preempt(j)
		} else {
			err = w.Submit(j)
		}
		app.mu.Unlock()

		switch {
		case errors.Is(err, worker.ErrClosed):
			continue
		case errors.Is(err, worker.ErrFull):
			j.Reply(apperror.New(429, "conversation queue full", fmt.Errorf("mailbox capacity %d", cfg.MailboxCap)))
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
