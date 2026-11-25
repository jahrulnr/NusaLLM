// Package appconversation is the broker between callers and per-room
// workers: it owns the worker map, submits jobs, and handles worker
// lifecycle (idle eviction, delete teardown). The use-case bodies live in
// application/job — this package only routes work to a room.
//
// Concurrency model: each conversation is owned by one room executor — a
// per-room serial executor. Send queues FIFO; Edit, Resume, and Delete
// preempt — they interrupt the running job and cut ahead of the wait
// line. Edit rewrites history; Resume continues it so a retry keeps the
// work the failed turn already paid for. Delete retires the worker:
// queued jobs get 410, and submits racing the teardown get ErrClosed and
// are reposted onto the fresh worker the next post spawns — a job that
// arrives after

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
)

// queuedJob is what a room's executor runs: the port's Job plus the result
// channel (Reply), room binding (Bind), and preemption intent (IsPreempt)
// the broker attaches.
type queuedJob interface {
	dservice.Job
	Reply(err error)
	Bind(convID string)
	IsPreempt() bool
}

// ConversationApp is the broker. cfg is the normalized conversation
// config (mailbox size feeds the rooms, turn limits ride inside deps);
// newWorker builds each room's executor, which workers maps conversation
// id → its live executor.
type ConversationApp struct {
	cfg         config.ConversationConfig
	configPath  string
	configStore config.Store
	deps        appjob.Deps
	newWorker   WorkerFactory
	mu          sync.Mutex
	workers     map[string]Worker

	shuttingDown bool
	shutdownDone chan struct{}
	workerWG     sync.WaitGroup
}

func New(
	convs drepo.Conversation,
	gateways drepo.Gateway,
	environments map[enum.Environment]appjob.EnvironmentDriver,
	cfg config.Config,
	configStore config.Store,
	configPath string,
	terminals dservice.TerminalSessions,
	summaryTool dservice.Tool,
	newWorker WorkerFactory,
) *ConversationApp {
	if newWorker == nil {
		// Composition error, not a runtime condition: without a factory
		// the broker cannot serve a room at all.
		panic("appconversation: New called without a worker factory")
	}
	// Normalize once at the boundary — invalid values clamp and zeros
	// pull their `default` tag, so every downstream read trusts the
	// value.
	ccfg := cfg.Conversation
	ccfg.Normalize()
	return &ConversationApp{
		cfg:         ccfg,
		configStore: configStore,
		configPath:  configPath,
		newWorker:   newWorker,
		deps: appjob.Deps{
			Convs:        convs,
			Gateways:     gateways,
			Environments: environments,
			Config:       ccfg,
			Terminals:    terminals,
			SummaryTool:  summaryTool,
		},
		workers: map[string]Worker{},
	}
}

// depsSnapshot returns a shallow copy of deps under app.mu so entry points
// do not read app.deps concurrently with the reload inside post.
func (app *ConversationApp) depsSnapshot() appjob.Deps {
	app.mu.Lock()
	defer app.mu.Unlock()
	return app.deps
}

// post enqueues a job on the room's executor, spawning one on demand, and
// answers the executor that accepted it — or nil when the post was refused
// (shutting down, reentrant, queue full). Preempt jobs (edit, delete) cut
// the line and interrupt whatever runs; ordinary jobs append FIFO. Posting
// stays under app.mu through Submit so Shutdown has a clear boundary
// between accepted and rejected work. A submit racing an executor's
// teardown gets ErrClosed — respawn and retry.
func (app *ConversationApp) post(convID string, j queuedJob) Worker {
	for {
		app.mu.Lock()
		// Live config rides each post — a settings change reaches the
		// next submitted job without restarting the broker; a failed
		// reload keeps the last good values.
		if app.configPath != "" {
			var fresh config.Config
			if err := fresh.Load(app.configStore, app.configPath); err == nil {
				ccfg := fresh.Conversation
				ccfg.Normalize()
				app.cfg = ccfg
				app.deps.Config = ccfg
			}
		}
		deps := app.deps
		cfg := app.cfg
		switch x := j.(type) {
		case *appjob.Send:
			x.Deps = deps
		case *appjob.Edit:
			x.Deps = deps
		case *appjob.Resume:
			x.Deps = deps
		case *appjob.Delete:
			x.Deps = deps
		case *appjob.Compact:
			x.Deps = deps
		case *appjob.Rename:
			x.Deps = deps
		}
		if app.shuttingDown {
			app.mu.Unlock()
			j.Reply(apperror.New(503, "conversation application is shutting down", nil))
			return nil
		}
		w := app.workers[convID]
		if w == nil || w.Dead() {
			w = app.newWorker(dservice.WorkerOptions{
				QueueCap: cfg.MailboxCap,
				OnIdle:   func(idle dservice.Retirable) { app.evict(convID, idle) },
				OnTerminate: func(term dservice.Retirable, drained []dservice.Job) {
					// A self-terminated executor (a delete job retired it):
					// queued jobs die with the room — 410 Gone — and the
					// map entry goes, so the next post spawns a fresh
					// executor.
					gone := apperror.New(410, "conversation deleted", fmt.Errorf("room %s removed", convID))
					for _, j := range drained {
						if aj, ok := j.(queuedJob); ok {
							aj.Reply(gone)
						}
					}
					app.mu.Lock()
					if app.workers[convID] == term {
						delete(app.workers, convID)
					}
					app.mu.Unlock()
				},
			})
			app.workerWG.Add(1)
			go func(w Worker) {
				<-w.Done()
				app.workerWG.Done()
			}(w)
			app.workers[convID] = w
		}

		// An event handler runs on the executor's own goroutine — a
		// synchronous same-room post from inside it would queue behind
		// the very job hosting the handler and deadlock until the
		// caller's ctx dies. Refuse instead; the handler must defer the
		// intent to its own goroutine.
		if w.OnWorkerGoroutine() {
			app.mu.Unlock()
			j.Reply(apperror.New(409, "cannot post to a conversation from inside its own turn", fmt.Errorf("reentrant same-room post on %s", convID)))
			return nil
		}

		j.Bind(convID)
		var err error
		if j.IsPreempt() {
			err = w.Preempt(j)
		} else {
			err = w.Submit(j)
		}
		app.mu.Unlock()

		switch {
		case errors.Is(err, dservice.ErrClosed):
			continue
		case errors.Is(err, dservice.ErrFull):
			j.Reply(apperror.New(429, "conversation queue full", fmt.Errorf("mailbox capacity %d", cfg.MailboxCap)))
			return nil
		}
		return w
	}
}

// evict retires an idle executor — called by the executor's OnIdle hook and
// from paths that discover an idle one (e.g. Cancel finding nothing
// running). Anything submitted in the race window lands in Terminate's
// drain and is reposted onto the successor.
func (app *ConversationApp) evict(convID string, w dservice.Retirable) {
	app.mu.Lock()
	live := app.workers[convID]
	if live == nil || live != w || !w.Idle() {
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
