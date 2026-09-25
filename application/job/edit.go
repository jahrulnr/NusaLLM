package job

import (
	"context"
	"errors"

	"github.com/jahrulnr/llm-base/infrastructure/domain/aggregate"
	"github.com/jahrulnr/llm-base/infrastructure/domain/event"
	apperror "github.com/jahrulnr/llm-base/infrastructure/error"
)

// Edit rewrites one user message and regenerates the turn from it —
// everything recorded after the target is dropped. It preempts: the
// running job is interrupted and the edit cuts ahead of queued sends.
// 400 for a non-user target, 404 when the id exists nowhere in history —
// a sealed target is repromoted by the repository.
type Edit struct {
	Base
	Deps    Deps
	Ctx     context.Context
	MsgID   string
	Content string
	OnEvent event.TurnEventHandler
}

// IsPreempt marks edit as a control job — it cuts the queue and
// interrupts whatever is running.
func (j *Edit) IsPreempt() bool { return true }

// Execute applies the rewrite on the worker's goroutine, then resends.
func (j *Edit) Execute(execCtx context.Context) (err error) {
	defer j.finish(&err)
	ctx := j.Cancellable(execCtx, j.Ctx)
	d := j.Deps
	convID := j.ConvID()
	err = func() error {
		if err := ctx.Err(); err != nil {
			return apperror.New(408, "edit canceled", err)
		}
		conv, err := d.Convs.Load(ctx, convID)
		if err != nil {
			return err // 404 — a room that isn't there has nothing to edit
		}
		switch err := conv.RewriteFrom(j.MsgID, j.Content); {
		case errors.Is(err, aggregate.ErrNotUserMessage):
			return apperror.New(400, "only user messages are editable", err)
		case errors.Is(err, aggregate.ErrMessageNotFound):
			// Not in the live tail — maybe sealed. The repository
			// repromotes the era holding it; reload so the aggregate
			// mirrors the new tail.
			if err := d.Convs.RewriteFrom(ctx, convID, j.MsgID, j.Content); err != nil {
				return err
			}
			conv, err = d.Convs.Load(ctx, convID)
			if err != nil {
				return err
			}
		case err != nil:
			return err
		default:
			if err := d.Convs.RewriteFrom(ctx, convID, j.MsgID, j.Content); err != nil {
				return err
			}
		}

		gw, model, svc, err := d.resolveRoute(ctx, convID, conv, "", "")
		if err != nil {
			return err
		}
		return d.runTurn(ctx, convID, conv, gw, model, svc, j.OnEvent)
	}()
	return err
}
