package job

import (
	"context"

	"github.com/jahrulnr/nusallm/infrastructure/domain/event"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
	"github.com/jahrulnr/nusallm/infrastructure/prompt"
)

// Resume continues a conversation's turn from its current history —
// unanswered tool calls finish first, then the model is called again.
// Unlike Edit it never rewrites or truncates: prior tool results and
// partial replies stay so a retry does not erase work the failed turn
// already paid for. It preempts: the running job is interrupted and the
// resume cuts ahead of queued sends.
//
// Route/ModelID/Opts are the same per-turn overrides Send takes — ""
// keeps the room's stored binding, and a successful resume re-binds
// them. Opts.Environment and Opts.Steering are send-only.
type Resume struct {
	Base
	Deps    Deps
	Ctx     context.Context
	Route   string
	ModelID string
	Opts    SendOptions
	OnEvent event.TurnEventHandler

	steers SteeringInput
}

// Steer buffers user input for the resumed turn — same contract as
// Send.Steer.
func (j *Resume) Steer(text string) { j.steers.push(text) }

// RecallSteer withdraws the newest un-fired steering input.
func (j *Resume) RecallSteer() (string, bool) { return j.steers.popLast() }

// IsPreempt marks resume as a control job — it cuts the queue and
// interrupts whatever is running.
func (j *Resume) IsPreempt() bool { return true }

// Execute continues the turn on the worker's goroutine.
func (j *Resume) Execute(execCtx context.Context) (err error) {
	defer j.finish(&err)
	ctx := j.Cancellable(execCtx, j.Ctx)
	d := j.Deps
	convID := j.ConvID()
	err = func() error {
		if err := ctx.Err(); err != nil {
			return apperror.New(408, "resume canceled", err)
		}
		conv, err := d.Convs.Load(ctx, convID)
		if err != nil {
			return err // 404 — nothing to resume
		}
		if len(conv.Messages()) == 0 {
			return apperror.New(400, "nothing to resume", nil)
		}
		if conv.SystemPrompt() == "" {
			conv.SetSystemPrompt(prompt.SystemPrompt("conversation"))
		}
		driver, err := d.environment(conv)
		if err != nil {
			return err
		}
		gw, model, svc, err := d.resolveRoute(ctx, convID, conv, driver, j.Route, j.ModelID)
		if err != nil {
			return err
		}
		route, modelID := gw.ID(), model.ID()
		if !j.Opts.Effort.IsUnset() {
			model.SetEffort(j.Opts.Effort)
		}
		model.SetUpstream(j.Opts.Upstream)
		if j.Opts.Workspace != "" {
			if driver.Workspace() == nil {
				return apperror.New(500, "environment workspace unavailable", nil)
			}
			root, err := driver.Workspace().CheckRoot(j.Opts.Workspace)
			if err != nil {
				return apperror.New(400, "invalid conversation workspace", err)
			}
			conv.SetWorkspace(root)
		}
		if err := d.runTurn(ctx, convID, conv, driver, gw, model, svc, j.OnEvent, &j.steers); err != nil {
			return err
		}
		conv.SetRoute(route)
		conv.SetModelID(modelID)
		return d.Convs.Save(ctx, conv)
	}()
	return err
}
