package job

import (
	"context"
	"fmt"
	"strings"

	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
	"github.com/jahrulnr/nusallm/infrastructure/prompt"
	toolsummary "github.com/jahrulnr/nusallm/infrastructure/service/tool/summary"
)

// compactAttempts bounds the checkpoint dialogue — a model that cannot
// produce the handoff in a few tries fails the compaction, not the turn
// forever.
const compactAttempts = 3

// maybeCompact runs compaction when the estimated context reaches the
// model's window share — caller derives the budget so headroom stays
// reserved for the reply. It reports whether history actually shrank:
// a turn that already compacted must not compact again on a provider
// 413 (one over-sized message would loop forever).
func (d Deps) maybeCompact(ctx context.Context, convID string, conv *aggregate.Conversation, gw *aggregate.Gateway, model entity.Model, svc dservice.TextModelService, budgetPct, keepTail int) (compacted bool, err error) {
	budget := model.CompactionBudgetTokens(budgetPct)
	if !conv.NeedCompaction(budget) || len(conv.Messages()) <= keepTail {
		// Nothing foldable: a checkpoint that drops zero messages is
		// noise in history, not headroom.
		return false, nil
	}
	return true, d.compact(ctx, convID, conv, gw, model, svc, keepTail)
}

// compact folds the live history into a checkpoint: the model writes the
// handoff on a scratch conversation (its own transcript plus the summary
// tool — never persisted), then the sealed prefix drops and the continue
// template becomes the new head of the live tail.
func (d Deps) compact(ctx context.Context, convID string, conv *aggregate.Conversation, gw *aggregate.Gateway, model entity.Model, svc dservice.TextModelService, keepTail int) error {
	scratch, err := aggregate.NewConversation(convID)
	if err != nil {
		return err
	}
	scratch.SetSystemPrompt(conv.SystemPrompt())
	scratch.SetWorkspace(conv.Workspace())
	if err := scratch.SetEnvironment(conv.Environment()); err != nil {
		return err
	}
	for _, m := range conv.Messages() {
		scratch.Append(m)
	}
	instruction, err := entity.NewMessage(enum.RoleUser, prompt.UserPrompt("compaction"))
	if err != nil {
		return err
	}
	instruction.SetUserType(enum.LLMUserTypeSystem)
	scratch.Append(instruction)
	summary := toolsummary.NewSummaryTool()
	scratch.SetTools([]valueobject.ToolSchema{summary.Schema()})

	checkpoint, err := d.writeCheckpoint(ctx, scratch, gw, model, svc, summary)
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
	dropBefore := len(conv.Messages()) - keepTail
	if dropBefore <= 0 {
		// Callers guard foldability, but a checkpoint that seals nothing
		// is noise — refuse rather than write an empty segment.
		return apperror.New(500, "nothing to compact", fmt.Errorf("dropBefore %d over %d messages", dropBefore, len(conv.Messages())))
	}
	if err := d.Convs.Compact(ctx, convID, next, dropBefore); err != nil {
		return err
	}
	conv.Compact(next, dropBefore)
	// Handoff spend merges only after the fold commits — a failed
	// Compact must not persist usage against an unchanged history.
	conv.AddUsage(scratch.Usage())
	if err := d.Convs.Save(ctx, conv); err != nil {
		return err
	}
	return nil
}

// writeCheckpoint drives the scratch dialogue: the model should answer
// with a single summary tool call, but a plain-text answer is accepted —
// a model that ignores the mandate still produced a checkpoint, which
// beats failing into a 413 loop.
func (d Deps) writeCheckpoint(ctx context.Context, scratch *aggregate.Conversation, gw *aggregate.Gateway, model entity.Model, svc dservice.TextModelService, summary *toolsummary.SummaryTool) (string, error) {
	for attempt := 0; attempt < compactAttempts; attempt++ {
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
			if call.Name() != toolsummary.Name {
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
}
