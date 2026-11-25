package cmdhelp

import (
	"context"
	"errors"
	"strings"

	appcommand "github.com/jahrulnr/nusallm/application/command"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
)

// HelpCommand implements /help.
type HelpCommand struct {
	h    appcommand.SlashSession
	list func() []dservice.CommandCompletion
}

// New binds /help; list supplies the current command catalog (from Describe).
func New(h appcommand.SlashSession, list func() []dservice.CommandCompletion) dservice.SlashCommand {
	return &HelpCommand{h: h, list: list}
}

func (c *HelpCommand) Name() string        { return "help" }
func (c *HelpCommand) Usage() string       { return "/help" }
func (c *HelpCommand) Description() string { return "show this command list" }

func (c *HelpCommand) Execute(_ context.Context, _ string, emitter dservice.CommandEmitter, args ...string) error {
	raw := ""
	if len(args) > 0 {
		raw = args[0]
	}
	if strings.TrimSpace(raw) != "" {
		return errors.New("usage: /help")
	}
	c.h.Write(emitter, "Commands:\n")
	for _, info := range c.list() {
		c.h.Write(emitter, "  %-24s %s\n", info.Usage, info.Desc)
	}
	c.h.Write(emitter, "Ctrl+C cancels the active turn; Ctrl+D exits at the prompt; Tab completes commands and their arguments.\n")
	c.h.Write(emitter, "Agent tool calls run automatically; exec is unsandboxed. Review command activity.\n")
	return nil
}
