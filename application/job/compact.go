package job

import (
	"context"
	"fmt"
	"strings"

	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/event"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
	"github.com/jahrulnr/nusallm/infrastructure/prompt"
)

// compactAttempts bounds the checkpoint dialogue — a model that cannot
// produce the handoff in a few tries fails the compaction, not the turn
// forever.
const compactAttempts = 3

// errNothingToCompact is the one answer to "this room has nothing to fold":
// a state the user can act on, so it maps to 400 invalid_argument at every
// transport — never a 500, which would report a perfectly valid request as a
// server fault. Both entry points into a fold (the manual job's pre-check and
// the fold itself) return it, so the two paths cannot disagree on status or
// on what the message says.
func errNothingToCompact(convID string, messages, keepTail int) error {
	// The numbers ride in the message, not only the cause: the transport
	// shows the message to the user, and the reason is what makes a
	// refusal actionable.
	return apperror.New(400,
		fmt.Sprintf("nothing to compact: %d message(s) with keep-tail %d", messages, keepTail),
		fmt.Errorf("conversation %s has no new foldable history", convID))
}

// compact folds the live history into a checkpoint: the model writes the
// handoff on a scratch conversation (its own transcript plus the summary
// tool — never persisted), then the sealed prefix drops and the continue
// template becomes the new head of the live tail.
// onEvent is the turn's stream when the fold runs inside a turn — the
// fold can take a whole provider call, so the owner is told it is
// happening. A manual compaction passes nil.
func (d Deps) compact(ctx context.Context, convID string, conv *aggregate.Conversation, gw *aggregate.Gateway, model entity.Model, svc dservice.TextModelService, keepTail int, onEvent event.TurnEventHandler) error {
	// The fold decision is the aggregate's, taken before the checkpoint
	// dialogue spends a provider call: a request that would seal nothing
	// is refused with the caller's own status.
	if !conv.CanManualCompact(keepTail) {
		return errNothingToCompact(convID, len(conv.Messages()), keepTail)
	}
	if onEvent != nil {
		onEvent(event.NewStatusEvent(enum.TurnStatusCompacting, 0))
	}
	scratch, err := aggregate.NewConversation(convID)
	if err != nil {
		return err
	}
	scratch.SetSystemPrompt(conv.SystemPrompt())
	scratch.SetWorkspace(conv.Workspace())
	if err := scratch.SetEnvironment(conv.Environment()); err != nil {
		return err
	}
	prefix := conv.Messages()
	if sanitizer, ok := svc.(dservice.CompactionSanitizer); ok {
		prefix = sanitizer.SanitizeCompactedTail(prefix)
	}
	if len(prefix) != len(conv.Messages()) {
		return apperror.New(500, "compaction sanitizer changed history length",
			fmt.Errorf("got %d messages, want %d", len(prefix), len(conv.Messages())))
	}
	for _, m := range prefix {
		scratch.Append(m)
	}
	instruction, err := entity.NewMessage(enum.RoleUser, prompt.UserPrompt("compaction"))
	if err != nil {
		return err
	}
	instruction.SetUserType(enum.LLMUserTypeSystem)
	scratch.Append(instruction)
	summary := d.SummaryTool
	if summary == nil {
		return apperror.New(500, "compaction unavailable", fmt.Errorf("no summary tool injected"))
	}
	scratch.SetTools([]valueobject.ToolSchema{summary.Schema()})

	// The scratch dialogue should answer with a single summary tool call,
	// but a plain-text answer is accepted — a model that ignores the
	// mandate still produced a checkpoint, which beats failing into a
	// 413 loop.
	checkpoint, err := func() (string, error) {
		for range compactAttempts {
			if err := ctx.Err(); err != nil {
				return "", apperror.New(408, "compaction canceled", err)
			}
			before := len(scratch.Messages())
			if err := svc.Send(ctx, scratch, gw, model.ApplyUnknownContextDefaults(), nil); err != nil {
				return "", err
			}
			msgs := scratch.Messages()
			if len(msgs) == before {
				return "", apperror.New(500, "compaction adapter produced no message", fmt.Errorf("service appended 0 messages"))
			}
			last := &msgs[len(msgs)-1]
			pending := pendingCalls(*last)
			if len(pending) == 0 {
				if text := strings.TrimSpace(last.Content()); text != "" {
					return text, nil
				}
				continue // an empty message deserves another attempt, not a checkpoint
			}
			for _, call := range pending {
				if call.Name() != summary.Schema().Name() {
					_ = last.SetToolOutput(call.ID(), "error: only the summary tool is available")
					continue
				}
				if res, err := summary.Execute(ctx, call, nil); err == nil {
					return res.Text, nil
				}
				_ = last.SetToolOutput(call.ID(), "error: the text field must hold the checkpoint")
			}
		}
		return "", apperror.New(500, "compaction failed", fmt.Errorf("model produced no checkpoint in %d attempts", compactAttempts))
	}()
	if err != nil {
		return err
	}

	next, err := entity.NewMessage(enum.RoleUser,
		strings.Replace(prompt.UserPrompt("compacted-continue"), "{{compacted_summaries}}", checkpoint, 1))
	if err != nil {
		return err
	}
	next.SetUserType(enum.LLMUserTypeSystem)

	// Keep the most recent messages live; the checkpoint is written for
	// exactly this tail plus it. Tool outputs ride inside their call's
	// message in this domain, so a message boundary cannot split a
	// call/result pair.
	// Foldability was decided before the checkpoint dialogue, so the
	// sealed prefix is non-empty by construction.
	dropBefore := len(conv.Messages()) - keepTail
	tail := conv.Messages()[dropBefore:]
	if sanitizer, ok := svc.(dservice.CompactionSanitizer); ok {
		tail = sanitizer.SanitizeCompactedTail(tail)
	}
	if len(tail) != len(conv.Messages())-dropBefore {
		return apperror.New(500, "compaction sanitizer changed retained tail",
			fmt.Errorf("got %d messages, want %d", len(tail), len(conv.Messages())-dropBefore))
	}
	if err := d.Convs.Compact(ctx, convID, next, dropBefore, tail); err != nil {
		return err
	}
	conv.Compact(next, dropBefore, tail)
	// Handoff spend merges only after the fold commits — a failed
	// Compact must not persist usage against an unchanged history.
	conv.AddUsage(scratch.Usage())
	if err := d.Convs.Save(ctx, conv); err != nil {
		return err
	}
	return nil
}
