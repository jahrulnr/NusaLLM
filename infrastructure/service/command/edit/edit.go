package cmdedit

import (
	"context"
	"errors"
	"strings"
	"unicode"

	appcommand "github.com/jahrulnr/nusallm/application/command"
	"github.com/jahrulnr/nusallm/infrastructure/domain/event"
	"github.com/jahrulnr/nusallm/infrastructure/domain/service"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
	svccommand "github.com/jahrulnr/nusallm/infrastructure/service/command"
)

// EditCommand is the /edit command. An edit regenerates the turn, so the
// command is worker-bound: Job validates the line and binds the closure,
// Registry.Job hands it to transports that run jobs asynchronously, and
// Execute runs the same closure inline.
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

// Job validates the arguments and binds the edit turn. The closure takes
// the transport's event handler because an edit streams like a send.
func (c *EditCommand) Job(args ...string) (svccommand.JobFunc, error) {
	raw := ""
	if len(args) > 0 {
		raw = args[0]
	}
	if c.h.ConversationID() == "" {
		return nil, errors.New("no active conversation; use /resume <id>")
	}
	messageID, content, ok := valueobject.NewCommand(raw).SplitFirst()
	if !ok || strings.TrimSpace(content) == "" {
		return nil, errors.New("usage: /edit <message-id> <replacement text>")
	}
	convID := c.h.ConversationID()
	return func(ctx context.Context, _ dservice.CommandEmitter, onEvent event.TurnEventHandler) error {
		return c.h.EditConversation(ctx, convID, messageID, content, onEvent)
	}, nil
}

func (c *EditCommand) Execute(ctx context.Context, _ string, emitter dservice.CommandEmitter, args ...string) error {
	job, err := c.Job(args...)
	if err != nil {
		return err
	}
	return job(ctx, emitter, nil)
}
