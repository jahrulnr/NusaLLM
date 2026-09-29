package commandinternal

import (
	"strings"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
)

const listTitleFallback = "untitled"

// TitleUnset reports whether a stored title needs a history-derived label.
func TitleUnset(title string) bool {
	title = strings.TrimSpace(title)
	return title == "" || strings.EqualFold(title, listTitleFallback)
}

// ListDisplayTitle resolves the label shown in conversation command lists.
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

// ListDisplayTitleMatchesPrefix filters command choices by id or title.
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
