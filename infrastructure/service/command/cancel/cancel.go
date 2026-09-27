package cmdcancel

import (
	"context"
	"errors"
	"strings"

	appcommand "github.com/jahrulnr/llm-base/application/command"
	dservice "github.com/jahrulnr/llm-base/infrastructure/domain/service"
)

// CancelCommand is the /cancel command.
type CancelCommand struct{ h appcommand.SlashSession }

// New binds /cancel to a session host.
func New(h appcommand.SlashSession) dservice.SlashCommand { return &CancelCommand{h: h} }

func (c *CancelCommand) Name() string        { return "cancel" }
func (c *CancelCommand) Usage() string       { return "/cancel" }
func (c *CancelCommand) Description() string { return "interrupt the active turn" }

func (c *CancelCommand) Execute(_ context.Context, _ string, emitter dservice.CommandEmitter, args ...string) error {
	raw := ""
	if len(args) > 0 {
		raw = args[0]
	}
	if strings.TrimSpace(raw) != "" {
		return errors.New("usage: /cancel")
	}
	if !c.h.CancelTurnActive() {
		c.h.Write(emitter, "no active turn\n")
		return nil
	}
	c.h.RequestCancelTurn()
	c.h.Write(emitter, "interrupt requested\n")
	return nil
}
