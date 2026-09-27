package cmdedit

import (
	"context"
	"errors"
	"strings"
	"unicode"

	appcommand "github.com/jahrulnr/nusallm/application/command"
	"github.com/jahrulnr/nusallm/infrastructure/domain/service"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

// EditCommand is the /edit command. The TUI also handles bare /edit and
// picker fills in delivery/cli/tui/model.beginEdit; keep usage text and
// editability rules aligned with that path.
type EditCommand struct{ h appcommand.SlashSession }

// New binds /edit to a session host.
func New(h appcommand.SlashSession) dservice.SlashCommand { return &EditCommand{h: h} }

func (c *EditCommand) Name() string        { return "edit" }
func (c *EditCommand) Usage() string       { return "/edit <id> <text>" }
func (c *EditCommand) Description() string { return "replace a message and regenerate" }

func (c *EditCommand) Complete(ctx context.Context, arg string) []dservice.CommandCompletion {
	if c.h.ConversationID() == "" || strings.IndexFunc(arg, unicode.IsSpace) >= 0 {
		return nil
	}
	messages, err := c.h.ConversationHistory(ctx, c.h.ConversationID(), -50, 0)
	if err != nil {
		return nil
	}
	arg = strings.ToLower(arg)
	var rows []service.CommandCompletion
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		if !message.Editable() {
			continue
		}
		if arg != "" &&
			!strings.Contains(strings.ToLower(message.ID()), arg) &&
			!strings.Contains(strings.ToLower(message.Content()), arg) {
			continue
		}
		preview := c.h.SanitizeText(message.Content())
		if r := []rune(preview); len(r) > 40 {
			preview = string(r[:40]) + "…"
		}
		rows = append(rows, service.CommandCompletion{Usage: "/edit " + message.ID() + " ", Desc: preview, FillOnly: true})
	}
	if len(rows) == 0 {
		return nil
	}
	return rows
}

func (c *EditCommand) Execute(ctx context.Context, _ string, emitter dservice.CommandEmitter, args ...string) error {
	raw := ""
	if len(args) > 0 {
		raw = args[0]
	}
	if c.h.ConversationID() == "" {
		return errors.New("no active conversation; use /resume <id>")
	}
	messageID, content, ok := valueobject.NewCommand(raw).SplitFirst()
	if !ok || strings.TrimSpace(content) == "" {
		return errors.New("usage: /edit <message-id> <replacement text>")
	}
	return c.h.EditConversation(ctx, c.h.ConversationID(), messageID, content, nil)
}
