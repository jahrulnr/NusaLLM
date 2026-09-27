package aggregate

import (
	"strings"

	"github.com/jahrulnr/llm-base/infrastructure/domain/entity"
)

const listTitleFallback = "untitled"

// TitleUnset reports whether a stored conversation title is absent for list
// display — empty and the literal "untitled" both mean unset.
func TitleUnset(title string) bool {
	title = strings.TrimSpace(title)
	return title == "" || strings.EqualFold(title, listTitleFallback)
}

// ListDisplayTitle resolves the label shown in conversation lists. When the
// stored title is unset, the first editable (user) message supplies the
// text; otherwise the stored title is normalized. Sanitization for a
// specific UI belongs in that transport.
func ListDisplayTitle(storedTitle string, messages []entity.Message) string {
	title := strings.TrimSpace(storedTitle)
	if TitleUnset(title) {
		for _, message := range messages {
			if message.Editable() {
				title = strings.TrimSpace(message.Content())
				break
			}
		}
	}
	if TitleUnset(title) {
		return listTitleFallback
	}
	return strings.Join(strings.Fields(title), " ")
}

// ListDisplayTitleMatchesPrefix reports whether a conversation list row
// matches a case-insensitive id or title prefix — used by pickers that
// filter on conversation id, stored title, or resolved display title.
func ListDisplayTitleMatchesPrefix(convID, storedTitle, displayTitle, prefix string) bool {
	prefix = strings.ToLower(strings.TrimSpace(prefix))
	if prefix == "" {
		return true
	}
	if strings.Contains(strings.ToLower(convID), prefix) {
		return true
	}
	stored := strings.TrimSpace(storedTitle)
	if !TitleUnset(stored) && strings.Contains(strings.ToLower(stored), prefix) {
		return true
	}
	return strings.Contains(strings.ToLower(displayTitle), prefix)
}
