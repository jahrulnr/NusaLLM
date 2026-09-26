package appconversation

import (
	"context"

	"github.com/jahrulnr/llm-base/infrastructure/domain/entity"
	drepo "github.com/jahrulnr/llm-base/infrastructure/domain/repository"
)

// Info returns the lightweight projection for one conversation.
func (app *ConversationApp) Info(ctx context.Context, id string) (drepo.ConversationInfo, error) {
	return app.deps.Convs.Info(ctx, id)
}

// List returns conversation projections ordered by recent activity.
func (app *ConversationApp) List(ctx context.Context) ([]drepo.ConversationInfo, error) {
	return app.deps.Convs.List(ctx)
}

// History returns a ranged slice of the conversation's global message history.
func (app *ConversationApp) History(ctx context.Context, id string, start, end int) ([]entity.Message, error) {
	return app.deps.Convs.LoadAt(ctx, id, start, end)
}
