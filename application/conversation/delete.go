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
// the room; queued jobs get 410. Afterwards the worker retires and the id
// is free again — a new Send with the same id mints a fresh room.
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
		return err
	case <-ctx.Done():
		return apperror.New(408, "delete request canceled", ctx.Err())
	}
}
