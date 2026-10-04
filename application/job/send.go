package job

import (
	"context"
	"strings"
	"sync"

	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/event"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
	"github.com/jahrulnr/nusallm/infrastructure/prompt"
)

// PersistenceReceipt records whether one Send appended its user message.
// The broker reads it even when cancellation wins the wait, while Send
// serializes the append and receipt update against that read.
type PersistenceReceipt struct {
	mu                   sync.Mutex
	userMessagePersisted bool
}

// NewPersistenceReceipt creates a receipt for one Send submission.
func NewPersistenceReceipt() *PersistenceReceipt { return &PersistenceReceipt{} }

// UserMessagePersisted reports whether this submission's user line committed.
func (r *PersistenceReceipt) UserMessagePersisted() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.userMessagePersisted
}

func (r *PersistenceReceipt) appendUserMessage(appendMessage func() (bool, error)) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	persisted, err := appendMessage()
	r.userMessagePersisted = persisted
	return err
}

// SendOptions carries the UI's per-turn selections — reasoning effort,
// an aggregator upstream pin, and the workspace the room's tools work
// in. Zero values keep the conversation's stored binding.
type SendOptions struct {
	Effort      enum.Effort      // unset/default = the model's configured effort
	Upstream    string           // aggregator backend name; "" = the gateway decides
	Workspace   string           // absolute dir; "" = keep the room's binding
	Environment enum.Environment // "" = keep the room's binding (local for a new room)
}

// Send is one user turn as a queued command: it validates routing,
// persists the user message, then runs the turn loop until the model
// stops asking for tools or the step cap is hit. Ctx is the caller's —
// a queued send abandons itself via it; Interrupt cancels the run.
type Send struct {
	Base
	Deps    Deps
	Ctx     context.Context
	Receipt *PersistenceReceipt // per-submission append state shared with the broker

	Route       string
	ModelID     string
	Message     string
	Attachments []valueobject.Attachment
	Opts        SendOptions
	OnEvent     event.TurnEventHandler

	steers steerInbox
}

// Steer buffers user input for the running turn — the worker's
// out-of-band control path. The turn loop folds it at the next safe
// point; it never interrupts the in-flight provider call.
func (j *Send) Steer(text string) { j.steers.push(text) }

// RecallSteer withdraws the newest un-fired steering input.
func (j *Send) RecallSteer() (string, bool) { return j.steers.popLast() }

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

		if j.Opts.Environment != "" {
			previous := conv.Environment()
			if err := conv.SetEnvironment(j.Opts.Environment); err != nil {
				return apperror.New(400, "unknown conversation environment", err)
			}
			if !fresh && previous != j.Opts.Environment {
				conv.SetWorkspace("")
			}
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
		// Per-turn selections override the catalog entry in memory; the
		// room's workspace rebinds the same way route/modelID do below.
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
			if title := strings.TrimSpace(user.Content()); title != "" {
				conv.SetTitle(title)
			}
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
		if j.Receipt == nil {
			return apperror.NewInternalError("send persistence receipt missing", nil)
		}
		if err := j.Receipt.appendUserMessage(func() (bool, error) {
			if err := ctx.Err(); err != nil {
				return false, apperror.New(408, "send canceled", err)
			}
			return d.Convs.AppendMessage(ctx, convID, user)
		}); err != nil {
			if fresh && !j.Receipt.UserMessagePersisted() {
				_ = d.Convs.Delete(ctx, convID) // roll back an uncommitted room
			}
			return err
		}

		if err := d.runTurn(ctx, convID, conv, driver, gw, model, svc, j.OnEvent, &j.steers); err != nil {
			return err
		}
		conv.SetRoute(route)     // existing rooms re-bind only on success —
		conv.SetModelID(modelID) // a failed override keeps the old binding
		return d.Convs.Save(ctx, conv)
	}()
	return err
}
