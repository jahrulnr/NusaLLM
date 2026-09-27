package commandinternal

import (
	appcommand "github.com/jahrulnr/llm-base/application/command"
	"github.com/jahrulnr/llm-base/infrastructure/domain/entity"
	"github.com/jahrulnr/llm-base/infrastructure/domain/service"
)

// Write formats a message transcript for slash-command output (terminal
// sanitization via the host). This is presentation, not domain state.
func Write(h appcommand.SlashSession, emitter service.CommandEmitter, messages []entity.Message) {
	for _, message := range messages {
		h.Write(emitter, "%s %s", message.ID(), message.Role())
		if message.Content() != "" {
			h.Write(emitter, ": %s", h.SanitizeText(message.Content()))
		}
		h.Write(emitter, "\n")
		for _, attachment := range message.Attachments() {
			h.Write(emitter, "  [attachment %s %s]\n", h.SanitizeText(attachment.Name()), attachment.Modality())
		}
		for _, call := range message.ToolCalls() {
			h.Write(emitter, "  [tool %s %s] %s\n", h.SanitizeText(call.Name()), h.SanitizeText(call.ID()), h.SanitizeText(call.Input()))
			if call.Output() != "" {
				h.Write(emitter, "  [tool result %s] %s\n", h.SanitizeText(call.ID()), h.SanitizeText(call.Output()))
			}
			for _, attachment := range call.OutputAttachments() {
				h.Write(emitter, "  [tool attachment %s %s]\n", h.SanitizeText(attachment.Name()), attachment.Modality())
			}
		}
	}
}
