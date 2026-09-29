package job

import (
	"context"
	"fmt"

	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
)

// Compact folds the live tail into a checkpoint on demand — the manual
// counterpart to the budget-triggered path in send. It queues like a
// send (never preempts): folding while a turn is mid-flight would race
// the mutations the worker is writing.
type Compact struct {
	Base
	Deps Deps
	Ctx  context.Context
}

// Execute runs the checkpoint dialogue on the worker's goroutine.
func (j *Compact) Execute(execCtx context.Context) (err error) {
	defer j.finish(&err)
	ctx := j.Cancellable(execCtx, j.Ctx)
	d := j.Deps
	convID := j.ConvID()
	err = func() error {
		if err := ctx.Err(); err != nil {
			return apperror.New(408, "compaction canceled", err)
		}
		conv, err := d.Convs.Load(ctx, convID)
		if err != nil {
			return err // 404 — no room, nothing to fold
		}
		keepTail := d.Config.CompactKeepTail
		if !conv.CanManualCompact(keepTail) {
			return apperror.New(400, "nothing to compact",
				fmt.Errorf("conversation %s has no new foldable history (messages %d, keep-tail %d)", convID, len(conv.Messages()), keepTail))
		}
		driver, err := d.environment(conv)
		if err != nil {
			return err
		}
		gw, model, svc, err := d.resolveRoute(ctx, convID, conv, driver, "", "")
		if err != nil {
			return err
		}
		return d.compact(ctx, convID, conv, gw, model.ApplyUnknownContextDefaults(), svc, keepTail)
	}()
	return err
}

// Rename retitles a room — a metadata write queued on the worker so it
// cannot race a turn's own meta writes.
type Rename struct {
	Base
	Deps  Deps
	Ctx   context.Context
	Title string
}

// Execute applies the title on the worker's goroutine.
func (j *Rename) Execute(execCtx context.Context) (err error) {
	defer j.finish(&err)
	ctx := j.Cancellable(execCtx, j.Ctx)
	d := j.Deps
	err = func() error {
		if err := ctx.Err(); err != nil {
			return apperror.New(408, "rename canceled", err)
		}
		conv, err := d.Convs.Load(ctx, j.ConvID())
		if err != nil {
			return err
		}
		conv.SetTitle(j.Title)
		return d.Convs.Save(ctx, conv)
	}()
	return err
}
