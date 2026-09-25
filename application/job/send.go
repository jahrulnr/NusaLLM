package job

import (
	"context"

	"github.com/jahrulnr/llm-base/infrastructure/domain/aggregate"
	"github.com/jahrulnr/llm-base/infrastructure/domain/entity"
	"github.com/jahrulnr/llm-base/infrastructure/domain/enum"
	"github.com/jahrulnr/llm-base/infrastructure/domain/event"
	"github.com/jahrulnr/llm-base/infrastructure/domain/valueobject"
	apperror "github.com/jahrulnr/llm-base/infrastructure/error"
	"github.com/jahrulnr/llm-base/infrastructure/prompt"
)

// Send is one user turn as a queued command: it validates routing,
// persists the user message, then runs the turn loop until the model
// stops asking for tools or the step cap is hit. Ctx is the caller's —
// a queued send abandons itself via it; Interrupt cancels the run.
type Send struct {
	Base
	Deps        Deps
	Ctx         context.Context
	Route       string
	ModelID     string
	Message     string
	Attachments []valueobject.Attachment
	OnEvent     event.TurnEventHandler
}

// Execute runs the whole turn end-to-end on the worker's goroutine.
func (j *Send) Execute(execCtx context.Context) (err error) {
	defer j.finish(&err)
	ctx := j.Cancellable(execCtx, j.Ctx)
	d := j.Deps
	convID := j.ConvID()
	err = func() error {
		if err := ctx.Err(); err != nil {
			return apperror.New(408, "send canceled", err)
		}

		conv, err := d.Convs.Load(ctx, convID)
		fresh := false
		if err != nil {
			if apperror.As(err).StatusCode() != apperror.StatusNotFound {
				return err
			}
			conv, err = aggregate.NewConversation(convID) // FE-anchored or BE-minted id
			if err != nil {
				return apperror.New(400, "invalid conversation id", err)
			}
			fresh = true
		}

		gw, model, svc, err := d.resolveRoute(ctx, convID, conv, j.Route, j.ModelID)
		if err != nil {
			return err
		}
		route, modelID := gw.ID(), model.ID()

		user, err := entity.NewMessage(enum.RoleUser, j.Message)
		if err != nil {
			return err
		}
		for _, a := range j.Attachments {
			user.AddAttachment(a)
		}
		conv.Append(user)
		if fresh {
			// Bind before the first write — a failed send still leaves a
			// room that can retry with route/modelID "".
			conv.SetRoute(route)
			conv.SetModelID(modelID)
			if err := d.Convs.Save(ctx, conv); err != nil {
				return err
			}
		}
		// The system prompt is code, not data: it ships embedded and is
		// applied to the live aggregate every turn, never persisted — a
		// template upgrade reaches every room on its next send.
		if conv.SystemPrompt() == "" {
			conv.SetSystemPrompt(prompt.SystemPrompt("conversation"))
		}
		if err := d.Convs.AppendMessage(ctx, convID, user); err != nil {
			if fresh {
				_ = d.Convs.Delete(ctx, convID) // roll back the orphan room
			}
			return err
		}

		if err := d.runTurn(ctx, convID, conv, gw, model, svc, j.OnEvent); err != nil {
			return err
		}
		conv.SetRoute(route)     // existing rooms re-bind only on success —
		conv.SetModelID(modelID) // a failed override keeps the old binding
		return d.Convs.Save(ctx, conv)
	}()
	return err
}
