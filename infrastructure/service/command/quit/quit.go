package cmdquit

import (
	"context"

	appcommand "github.com/jahrulnr/nusallm/application/command"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	cmdexit "github.com/jahrulnr/nusallm/infrastructure/service/command/exit"
)

// QuitCommand is an alias for /exit.
type QuitCommand struct {
	exit dservice.SlashCommand
}

// New binds /quit to the same behavior as /exit.
func New(h appcommand.SlashSession) dservice.SlashCommand {
	return &QuitCommand{exit: cmdexit.New(h)}
}

func (c *QuitCommand) Name() string        { return "quit" }
func (c *QuitCommand) Usage() string       { return "/quit" }
func (c *QuitCommand) Description() string { return "alias for /exit" }

func (c *QuitCommand) Execute(ctx context.Context, command string, emitter dservice.CommandEmitter, args ...string) error {
	return c.exit.Execute(ctx, command, emitter, args...)
}
