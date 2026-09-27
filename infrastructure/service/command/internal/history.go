package commandinternal

import (
	"strings"

	appcommand "github.com/jahrulnr/llm-base/application/command"
	"github.com/jahrulnr/llm-base/infrastructure/domain/entity"
	"github.com/jahrulnr/llm-base/infrastructure/domain/enum"
	"github.com/jahrulnr/llm-base/infrastructure/domain/service"
)

// Write formats a message transcript for slash-command output (terminal
// sanitization via the host). This is presentation, not domain state.
func Write(h appcommand.SlashSession, emitter service.CommandEmitter, messages []entity.Message) {
	for _, message := range messages {
		if message.Content() != "" {
			content := h.SanitizeText(message.Content())
			if message.Role() == enum.RoleUser {
				// Escape the Markdown blockquote marker so delivery can
				// render the same plain "> " chat prefix as a live turn.
				h.Write(emitter, "\\> %s\n\n", content)
			} else {
				h.Write(emitter, "%s\n\n", content)
			}
		}
		for _, attachment := range message.Attachments() {
			h.Write(emitter, "- attachment `%s` (%s)\n", h.SanitizeText(attachment.Name()), attachment.Modality())
		}
		for _, call := range message.ToolCalls() {
			h.Write(emitter, "⏺ `%s` `%s`\n\n", h.SanitizeText(call.Name()), h.SanitizeText(oneLine(call.Input())))
		}
		if message.Content() == "" && message.Role() == enum.RoleUser {
			h.Write(emitter, "\\>\n\n")
		}
	}
}

func oneLine(text string) string {
	const max = 160
	text = strings.Join(strings.Fields(text), " ")
	if len([]rune(text)) <= max {
		return text
	}
	runes := []rune(text)
	return string(runes[:max-1]) + "…"
}
