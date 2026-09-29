package commandinternal

import (
	"context"

	appcommand "github.com/jahrulnr/nusallm/application/command"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
)

// DisplayTitles resolves list labels for many conversations. Set titles skip
// history; unset titles load only a prefix of the transcript.
func DisplayTitles(ctx context.Context, h appcommand.SlashSession, conversations []drepo.ConversationInfo) map[string]string {
	out := make(map[string]string, len(conversations))
	for _, conv := range conversations {
		if !TitleUnset(conv.Title) {
			out[conv.ID] = ListDisplayTitle(conv.Title, nil)
		}
	}
	for _, conv := range conversations {
		if !TitleUnset(conv.Title) {
			continue
		}
		messages, err := h.ConversationHistory(ctx, conv.ID, 0, TitleHistoryEnd)
		if err != nil {
			out[conv.ID] = ListDisplayTitle(conv.Title, nil)
			continue
		}
		out[conv.ID] = ListDisplayTitle(conv.Title, messages)
	}
	return out
}

// Resolve loads a prefix of history when a command label needs it.
func Resolve(ctx context.Context, h appcommand.SlashSession, convID, storedTitle string) string {
	if !TitleUnset(storedTitle) {
		return ListDisplayTitle(storedTitle, nil)
	}
	messages, err := h.ConversationHistory(ctx, convID, 0, TitleHistoryEnd)
	if err != nil {
		return ListDisplayTitle(storedTitle, nil)
	}
	return ListDisplayTitle(storedTitle, messages)
}
