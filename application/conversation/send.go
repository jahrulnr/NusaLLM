package appconversation

import (
	"context"

	appjob "github.com/jahrulnr/llm-base/application/job"
	"github.com/jahrulnr/llm-base/infrastructure/domain/aggregate"
	"github.com/jahrulnr/llm-base/infrastructure/domain/event"
	"github.com/jahrulnr/llm-base/infrastructure/domain/valueobject"
	apperror "github.com/jahrulnr/llm-base/infrastructure/error"
)

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
// provider call aborts and returns 408, never an upstream error.
// A failed send leaves the user message persisted and no assistant turn;
// the room keeps its binding so a retry can reuse route/modelID "".
func (app *ConversationApp) Send(
	ctx context.Context,
	convID string,
	route string,
	modelID string,
	message string,
	attachments []valueobject.Attachment,
	onEvent event.TurnEventHandler,
) (string, error) {
	if convID == "" {
		c, err := aggregate.NewConversation("") // mints c_<base32>
		if err != nil {
			return "", err
		}
		convID = c.ID()
	}
	res := make(chan error, 1)
	j := &appjob.Send{
		Deps:        app.deps,
		Ctx:         ctx,
		Route:       route,
		ModelID:     modelID,
		Message:     message,
		Attachments: attachments,
		OnEvent:     onEvent,
	}
	j.Result = res
	app.post(convID, j)
	select {
	case err := <-res:
		return convID, err
	case <-ctx.Done():
		// The queued job notices the dead ctx on its own and no-ops; the
		// running one aborts through the same ctx — either way nothing
		// more is required here.
		return convID, apperror.New(408, "send canceled", ctx.Err())
	}
}
