package cmdclear

import (
	"context"
	"errors"
	"strings"

	appcommand "github.com/jahrulnr/nusallm/application/command"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
)

// ClearCommand is the /clear command.
type ClearCommand struct{ h appcommand.SlashSession }

// New binds /clear to a session host.
func New(h appcommand.SlashSession) dservice.SlashCommand { return &ClearCommand{h: h} }

func (c *ClearCommand) Name() string        { return "clear" }
func (c *ClearCommand) Usage() string       { return "/clear" }
func (c *ClearCommand) Description() string { return "clear the terminal screen" }

func (c *ClearCommand) Execute(_ context.Context, _ string, emitter dservice.CommandEmitter, args ...string) error {
	raw := ""
	if len(args) > 0 {
		raw = args[0]
	}
	if strings.TrimSpace(raw) != "" {
		return errors.New("usage: /clear")
	}
	if !c.h.Interactive() {
		c.h.Write(emitter, "clear needs an interactive terminal\n")
		return nil
	}
	c.h.RequestClearScreen()
	return nil
}
