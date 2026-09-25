package appconversation

import (
	"context"

	appjob "github.com/jahrulnr/llm-base/application/job"
	"github.com/jahrulnr/llm-base/infrastructure/domain/event"
	apperror "github.com/jahrulnr/llm-base/infrastructure/error"
)

// Edit rewrites a user message and resends the turn from it — the
// conversation is truncated at that message and the model regenerates the
// answer, so everything the old turn produced is gone.
//
// Semantics: only user messages are editable (400 otherwise). A target
// already compacted into a sealed segment is still reachable — the
// repository repromotes that era — but an id that exists nowhere is 404.
// A live turn on the conversation is interrupted first: the edit preempts
// it, then runs ahead of any queued sends, which land on the rewritten
// history. The caller's ctx aborts the wait and the regenerate alike.
func (app *ConversationApp) Edit(
	ctx context.Context,
	convID string,
	msgID string,
	content string,
	onEvent event.TurnEventHandler,
) error {
	res := make(chan error, 1)
	j := &appjob.Edit{
		Deps:    app.deps,
		Ctx:     ctx,
		MsgID:   msgID,
		Content: content,
		OnEvent: onEvent,
	}
	j.Result = res
	app.post(convID, j)
	select {
	case err := <-res:
		return err
	case <-ctx.Done():
		return apperror.New(408, "edit canceled", ctx.Err())
	}
}
