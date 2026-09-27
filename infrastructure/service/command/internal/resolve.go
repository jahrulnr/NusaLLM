package commandinternal

import (
	"context"

	appcommand "github.com/jahrulnr/llm-base/application/command"
	"github.com/jahrulnr/llm-base/infrastructure/domain/aggregate"
	drepo "github.com/jahrulnr/llm-base/infrastructure/domain/repository"
)

// DisplayTitles resolves list labels for many conversations. Set titles skip
// history; unset titles load only a prefix of the transcript.
func DisplayTitles(ctx context.Context, h appcommand.SlashSession, conversations []drepo.ConversationInfo) map[string]string {
	out := make(map[string]string, len(conversations))
	for _, conv := range conversations {
		if !aggregate.TitleUnset(conv.Title) {
			out[conv.ID] = aggregate.ListDisplayTitle(conv.Title, nil)
		}
	}
	for _, conv := range conversations {
		if !aggregate.TitleUnset(conv.Title) {
			continue
		}
		messages, err := h.ConversationHistory(ctx, conv.ID, 0, TitleHistoryEnd)
		if err != nil {
			// Fall back to aggregate rules (typically "untitled") when history is unavailable.
			out[conv.ID] = aggregate.ListDisplayTitle(conv.Title, nil)
			continue
		}
		out[conv.ID] = aggregate.ListDisplayTitle(conv.Title, messages)
	}
	return out
}

// Resolve loads a prefix of history when needed and applies aggregate list-title rules.
func Resolve(ctx context.Context, h appcommand.SlashSession, convID, storedTitle string) string {
	if !aggregate.TitleUnset(storedTitle) {
		return aggregate.ListDisplayTitle(storedTitle, nil)
	}
	messages, err := h.ConversationHistory(ctx, convID, 0, TitleHistoryEnd)
	if err != nil {
		return aggregate.ListDisplayTitle(storedTitle, nil)
	}
	return aggregate.ListDisplayTitle(storedTitle, messages)
}
