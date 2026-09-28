package appconversation

import (
	"context"

	appjob "github.com/jahrulnr/nusallm/application/job"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
)

// Info returns the lightweight projection for one conversation.
func (app *ConversationApp) Info(ctx context.Context, id string) (drepo.ConversationInfo, error) {
	return app.deps.Convs.Info(ctx, id)
}

// CanCompact reports whether manual compaction would fold new live history.
func (app *ConversationApp) CanCompact(ctx context.Context, id string) (bool, error) {
	conv, err := app.deps.Convs.Load(ctx, id)
	if err != nil {
		return false, err
	}
	return conv.CanManualCompact(app.deps.Config.CompactKeepTail), nil
}

// List returns conversation projections ordered by recent activity.
func (app *ConversationApp) List(ctx context.Context) ([]drepo.ConversationInfo, error) {
	return app.deps.Convs.List(ctx)
}

// History returns a ranged slice of the conversation's global message history.
func (app *ConversationApp) History(ctx context.Context, id string, start, end int) ([]entity.Message, error) {
	return app.deps.Convs.LoadAt(ctx, id, start, end)
}

// ToolSchemas returns the registered tool catalog, sorted by name — the
// same set the agent may call this turn.
func (app *ConversationApp) ToolSchemas() []valueobject.ToolSchema {
	return app.deps.ToolSchemas()
}

// Compact folds the live tail into a checkpoint on the room's worker —
// queued like a send so a running turn finishes first.
func (app *ConversationApp) Compact(ctx context.Context, convID string) error {
	res := make(chan error, 1)
	j := &appjob.Compact{Deps: app.deps, Ctx: ctx}
	j.Result = res
	app.post(convID, j)
	select {
	case err := <-res:
		return err
	case <-ctx.Done():
		return apperror.New(408, "compact request canceled", ctx.Err())
	}
}

// Rename retitles a conversation; the write is queued on the room's
// worker so it cannot race a turn's metadata saves.
func (app *ConversationApp) Rename(ctx context.Context, convID, title string) error {
	res := make(chan error, 1)
	j := &appjob.Rename{Deps: app.deps, Ctx: ctx, Title: title}
	j.Result = res
	app.post(convID, j)
	select {
	case err := <-res:
		return err
	case <-ctx.Done():
		return apperror.New(408, "rename request canceled", ctx.Err())
	}
}
