package cmddetach

import (
	"context"
	"errors"
	"strings"

	appcommand "github.com/jahrulnr/llm-base/application/command"
	dservice "github.com/jahrulnr/llm-base/infrastructure/domain/service"
)

// DetachCommand is the /detach command.
type DetachCommand struct{ h appcommand.SlashSession }

// New binds /detach to a session host.
func New(h appcommand.SlashSession) dservice.SlashCommand { return &DetachCommand{h: h} }

func (c *DetachCommand) Name() string        { return "detach" }
func (c *DetachCommand) Usage() string       { return "/detach" }
func (c *DetachCommand) Description() string { return "drop pending attachments" }

func (c *DetachCommand) Execute(_ context.Context, _ string, emitter dservice.CommandEmitter, args ...string) error {
	raw := ""
	if len(args) > 0 {
		raw = args[0]
	}
	if strings.TrimSpace(raw) != "" {
		return errors.New("usage: /detach")
	}
	if len(c.h.Attachments()) == 0 {
		c.h.Write(emitter, "no pending attachments\n")
		return nil
	}
	c.h.Write(emitter, "dropped %d attachment(s)\n", len(c.h.Attachments()))
	c.h.SetAttachments(nil)
	return nil
}
