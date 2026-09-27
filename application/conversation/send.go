package appconversation

import (
	"context"

	appjob "github.com/jahrulnr/nusallm/application/job"
	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/event"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
)

// SendResult identifies the conversation that received the submission and
// reports whether this submission's user message was persisted, even when
// Send returns an error.
type SendResult struct {
	ConversationID       string
	UserMessagePersisted bool
}

// Send delivers one user turn and returns the conversation id it landed
// in. The room is upserted: an existing convID appends, a new FE-minted
// convID creates it, and "" mints a fresh c_<base32> — the returned id
// always tells the caller which room received the message, so streamed
// events can be attributed. route/modelID "" reuse the stored binding;
// on a brand-new conversation both must be given.
//
// Concurrency contract: sends on one conversation queue FIFO on its
// worker, and the queue wait is cancellable through ctx — a send canceled
// while queued returns 408 having touched nothing. Once running, the send
// is reachable through Cancel: the user turn may already be persisted by
// then (it survives a canceled send — history stays honest), but the
// provider call aborts and returns 408, never an upstream error. The
// per-send persistence receipt keeps that distinction accurate if
// cancellation races with AppendMessage. A failure after the user append
// leaves that line in history without an assistant turn; earlier routing
// or validation failures do not persist it. The room keeps its binding
// after a persisted failed turn so a retry can reuse route/modelID "".
func (app *ConversationApp) Send(
	ctx context.Context,
	convID string,
	route string,
	modelID string,
	message string,
	attachments []valueobject.Attachment,
	onEvent event.TurnEventHandler,
) (SendResult, error) {
	if convID == "" {
		c, err := aggregate.NewConversation("") // mints c_<base32>
		if err != nil {
			return SendResult{}, err
		}
		convID = c.ID()
	}
	res := make(chan error, 1)
	receipt := appjob.NewPersistenceReceipt()
	j := &appjob.Send{
		Deps:    app.deps,
		Ctx:     ctx,
		Receipt: receipt,

		Route:       route,
		ModelID:     modelID,
		Message:     message,
		Attachments: attachments,
		OnEvent:     onEvent,
	}
	j.Result = res
	app.post(convID, j)
	result := SendResult{ConversationID: convID}
	select {
	case err := <-res:
		result.UserMessagePersisted = receipt.UserMessagePersisted()
		return result, err
	case <-ctx.Done():
		// Read through the receipt's append lock: if AppendMessage is in
		// flight, its commit decision must finish before this result is
		// returned. A queued job never reaches that append and stays false.
		result.UserMessagePersisted = receipt.UserMessagePersisted()
		return result, apperror.New(408, "send canceled", ctx.Err())
	}
}
