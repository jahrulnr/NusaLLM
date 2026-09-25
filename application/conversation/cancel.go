package appconversation

import (
	"context"
	"errors"
	"fmt"

	apperror "github.com/jahrulnr/llm-base/infrastructure/error"
	"github.com/jahrulnr/llm-base/infrastructure/worker"
)

// Cancel aborts the job currently running on the conversation's worker —
// a running Send returns 408 (canceled), not an upstream error, so
// fallback logic never mistakes it for a provider failure.
//
// Queued jobs are NOT cancelable here: Cancel targets the running job. A
// queued send is aborted through its own request context (the job checks
// it before running). 404 = nothing running.
func (app *ConversationApp) Cancel(ctx context.Context, convID string) error {
	app.mu.Lock()
	w := app.workers[convID]
	app.mu.Unlock()
	if w == nil {
		return apperror.NewNotFoundError("in-flight send "+convID, fmt.Errorf("nothing running"))
	}
	if err := w.Interrupt(); err != nil {
		if errors.Is(err, worker.ErrNotRunning) {
			app.evict(convID, w) // reap the idle worker while we're here
		}
		return apperror.NewNotFoundError("in-flight send "+convID, err)
	}
	return nil
}
