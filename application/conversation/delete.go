package appconversation

import (
	"context"

	appjob "github.com/jahrulnr/nusallm/application/job"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
)

// Delete removes the conversation and everything it stored — meta, the
// live tail, every sealed segment, and conversation-owned tool output.
// A running job is interrupted before
// the repository call, so an in-flight tool or provider request dies with
// the room; queued jobs get 410. Delete returns only once the retired
// worker's loop has exited and its map entry is gone — the id is free
// again, so a new Send with the same id mints a fresh room.
//
// The repository delete runs under a background context: a caller who
// abandons Delete mid-flight must not half-delete a room.
func (app *ConversationApp) Delete(ctx context.Context, convID string) error {
	res := make(chan error, 1)
	j := &appjob.Delete{}
	j.Result = res
	app.post(convID, j)
	select {
	case err := <-res:
		if err != nil {
			return err
		}
		// The job's reply outruns the retirement it triggers: finish
		// answers before the worker's loop exits, so a post landing in
		// that window could still Submit to the corpse and be drained
		// to a 410. Done closes only after OnTerminate dropped the map
		// entry — wait for it and the next post mints a fresh worker.
		if w := j.Worker(); w != nil {
			select {
			case <-w.Done():
			case <-ctx.Done():
				return apperror.New(408, "delete request canceled", ctx.Err())
			}
		}
		return nil
	case <-ctx.Done():
		return apperror.New(408, "delete request canceled", ctx.Err())
	}
}
