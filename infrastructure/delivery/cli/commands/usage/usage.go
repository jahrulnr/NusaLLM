package cmdusage

import (
	"context"
	"errors"
	"strings"

	appcommand "github.com/jahrulnr/nusallm/application/command"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
)

// UsageCommand is the /usage command.
type UsageCommand struct{ h appcommand.SlashSession }

// New binds /usage to a session host.
func New(h appcommand.SlashSession) dservice.SlashCommand { return &UsageCommand{h: h} }

func (c *UsageCommand) Name() string        { return "usage" }
func (c *UsageCommand) Usage() string       { return "/usage" }
func (c *UsageCommand) Description() string { return "show token accounting" }

func (c *UsageCommand) Execute(ctx context.Context, _ string, emitter dservice.CommandEmitter, args ...string) error {
	raw := ""
	if len(args) > 0 {
		raw = args[0]
	}
	if strings.TrimSpace(raw) != "" {
		return errors.New("usage: /usage")
	}
	if c.h.ConversationID() == "" {
		return errors.New("no active conversation; use /resume <id>")
	}
	info, err := c.h.ConversationInfo(ctx, c.h.ConversationID())
	if err != nil {
		return err
	}
	u := info.Usage
	c.h.Write(emitter, "input        %d\ncached read  %d\ncached write %d\noutput       %d\ntotal        %d\n",
		u.InputTokens(), u.CacheReadTokens(), u.CacheWriteTokens(), u.OutputTokens(), u.Total())
	return nil
}
