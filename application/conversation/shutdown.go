package appconversation

import (
	"context"

	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
	"github.com/jahrulnr/nusallm/infrastructure/worker"
)

// Shutdown rejects future posts, terminates every current room worker, and
// waits for their loops to exit. A canceled ctx stops only this caller's
// wait; the shared shutdown continues for concurrent and later callers.
func (app *ConversationApp) Shutdown(ctx context.Context) error {
	app.mu.Lock()
	if app.shutdownDone == nil {
		app.shuttingDown = true
		done := make(chan struct{})
		app.shutdownDone = done
		workers := make([]*worker.Worker, 0, len(app.workers))
		for _, w := range app.workers {
			workers = append(workers, w)
		}
		app.workers = make(map[string]*worker.Worker)
		go func() {
			stopped := apperror.New(503, "conversation application is shutting down", nil)
			for _, w := range workers {
				for _, j := range w.Terminate() {
					if aj, ok := j.(queuedJob); ok {
						aj.Reply(stopped)
					}
				}
			}
			for _, w := range workers {
				<-w.Done()
			}
			app.workerWG.Wait()
			close(done)
		}()
	}
	done := app.shutdownDone
	app.mu.Unlock()

	select {
	case <-done:
		return nil
	default:
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return apperror.New(408, "conversation shutdown canceled", ctx.Err())
	}
}
