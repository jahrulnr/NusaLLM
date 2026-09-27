package cmdcompact

import (
	"context"
	"errors"
	"strings"

	appcommand "github.com/jahrulnr/llm-base/application/command"
	dservice "github.com/jahrulnr/llm-base/infrastructure/domain/service"
)

// CompactCommand is the /compact command.
type CompactCommand struct{ h appcommand.SlashSession }

// New binds /compact to a session host.
func New(h appcommand.SlashSession) dservice.SlashCommand { return &CompactCommand{h: h} }

func (c *CompactCommand) Name() string        { return "compact" }
func (c *CompactCommand) Usage() string       { return "/compact" }
func (c *CompactCommand) Description() string { return "fold history into a checkpoint now" }

func (c *CompactCommand) Execute(ctx context.Context, _ string, emitter dservice.CommandEmitter, args ...string) error {
	raw := ""
	if len(args) > 0 {
		raw = args[0]
	}
	if strings.TrimSpace(raw) != "" {
		return errors.New("usage: /compact")
	}
	if c.h.ConversationID() == "" {
		return errors.New("no active conversation; use /resume <id>")
	}
	if err := c.h.CompactConversation(ctx, c.h.ConversationID()); err != nil {
		return err
	}
	c.h.Write(emitter, "compacted %s\n", c.h.ConversationID())
	return nil
}
