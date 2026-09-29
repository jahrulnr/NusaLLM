package cmdtools

import (
	"context"
	"errors"
	"strings"

	appcommand "github.com/jahrulnr/nusallm/application/command"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
)

// ToolsCommand is the /tools command.
type ToolsCommand struct{ h appcommand.SlashSession }

// New binds /tools to a session host.
func New(h appcommand.SlashSession) dservice.SlashCommand { return &ToolsCommand{h: h} }

func (c *ToolsCommand) Name() string        { return "tools" }
func (c *ToolsCommand) Usage() string       { return "/tools" }
func (c *ToolsCommand) Description() string { return "list the agent's tools" }

func (c *ToolsCommand) Execute(ctx context.Context, _ string, emitter dservice.CommandEmitter, args ...string) error {
	raw := ""
	if len(args) > 0 {
		raw = args[0]
	}
	if strings.TrimSpace(raw) != "" {
		return errors.New("usage: /tools")
	}
	schemas, err := c.h.ToolSchemas(ctx, c.h.ConversationID())
	if err != nil {
		return err
	}
	if len(schemas) == 0 {
		c.h.Write(emitter, "no tools registered\n")
		return nil
	}
	for _, s := range schemas {
		c.h.Write(emitter, "  %-14s %-12s %s\n", s.Name(), s.Permission(), s.Description())
	}
	return nil
}
