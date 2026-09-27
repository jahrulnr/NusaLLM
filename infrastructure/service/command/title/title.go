package cmdtitle

import (
	"context"
	"errors"
	"strings"

	appcommand "github.com/jahrulnr/llm-base/application/command"
	dservice "github.com/jahrulnr/llm-base/infrastructure/domain/service"
)

// TitleCommand is the /title command.
type TitleCommand struct{ h appcommand.SlashSession }

// New binds /title to a session host.
func New(h appcommand.SlashSession) dservice.SlashCommand { return &TitleCommand{h: h} }

func (c *TitleCommand) Name() string        { return "title" }
func (c *TitleCommand) Usage() string       { return "/title <text>" }
func (c *TitleCommand) Description() string { return "rename the conversation" }

func (c *TitleCommand) Execute(ctx context.Context, _ string, emitter dservice.CommandEmitter, args ...string) error {
	raw := ""
	if len(args) > 0 {
		raw = args[0]
	}
	title := strings.TrimSpace(raw)
	if title == "" {
		return errors.New("usage: /title <text>")
	}
	if c.h.ConversationID() == "" {
		return errors.New("no active conversation; use /resume <id>")
	}
	if err := c.h.RenameConversation(ctx, c.h.ConversationID(), title); err != nil {
		return err
	}
	c.h.Write(emitter, "title set to %q\n", title)
	return nil
}
