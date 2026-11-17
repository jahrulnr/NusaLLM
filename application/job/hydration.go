package job

import (
	"context"
	"slices"

	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

// appendToolContexts is the discovery phase before a provider call. The
// selected driver owns discovery and hands the turn the records it
// rendered; the application persists them verbatim, never importing a
// concrete tool adapter. Each record dedups against the newest live
// injection call carrying the same name and input — comparing
// Dedup-normalized output when the record declares a normalizer — so one
// pass re-records only semantically changed slots, volatile churn writes
// nothing, and compaction re-records restore the slots the fold sealed.
// Every changed record lands as its own TooltypeInjection call on a
// single system message.
func (d Deps) appendToolContexts(ctx context.Context, convID string, conv *aggregate.Conversation, driver EnvironmentDriver) error {
	contexts, err := driver.ToolContexts(ctx)
	if err != nil {
		return err
	}
	if len(contexts) == 0 {
		return nil
	}

	// Anchor each (name, input) pair on its newest recorded output —
	// scanning backward once so a same-name record bound to different
	// input never anchors another record's rescan.
	msgs := conv.Messages()
	latest := map[string]string{}
	for _, msg := range slices.Backward(msgs) {
		for _, prior := range msg.ToolCalls() {
			if prior.Type() != enum.TooltypeInjection || !prior.HasOutput() {
				continue
			}
			key := prior.Name() + "\x00" + prior.Input()
			if _, ok := latest[key]; !ok {
				latest[key] = prior.Output()
			}
		}
	}

	msg, err := entity.NewMessage(enum.RoleSystem, "")
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, record := range contexts {
		dedup := record.Dedup
		if dedup == nil {
			dedup = func(s string) string { return s }
		}
		key := record.Name + "\x00" + record.Input
		normalized := dedup(record.Output)
		if prior, ok := latest[key]; ok && dedup(prior) == normalized {
			continue
		}
		if seen[key] {
			continue
		}
		seen[key] = true

		id, err := valueobject.NewID("call", "")
		if err != nil {
			return err
		}
		call, err := entity.NewToolCall(id.String(), record.Name, record.Input, enum.TooltypeInjection)
		if err != nil {
			return err
		}
		call.SetStoredOutput(record.Output, "")
		msg.AddToolCall(call)
	}
	if len(msg.ToolCalls()) == 0 {
		return nil
	}
	conv.Append(msg)
	_, err = d.Convs.AppendMessage(ctx, convID, msg)
	return err
}
