package cmdexit

import (
	"context"
	"errors"
	"strings"

	appcommand "github.com/jahrulnr/nusallm/application/command"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
)

// ExitCommand is the /exit command.
type ExitCommand struct{ h appcommand.SlashSession }

// New binds /exit to a session host.
func New(h appcommand.SlashSession) dservice.SlashCommand { return &ExitCommand{h: h} }

func (c *ExitCommand) Name() string        { return "exit" }
func (c *ExitCommand) Usage() string       { return "/exit" }
func (c *ExitCommand) Description() string { return "leave the REPL" }

func (c *ExitCommand) Execute(_ context.Context, _ string, emitter dservice.CommandEmitter, args ...string) error {
	raw := ""
	if len(args) > 0 {
		raw = args[0]
	}
	if strings.TrimSpace(raw) != "" {
		return errors.New("usage: /exit")
	}
	c.h.SetExit(true)
	c.h.Write(emitter, "goodbye\n")
	return nil
}
