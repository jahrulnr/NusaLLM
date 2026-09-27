package cmdpwd

import (
	"context"
	"errors"
	"strings"

	appcommand "github.com/jahrulnr/llm-base/application/command"
	dservice "github.com/jahrulnr/llm-base/infrastructure/domain/service"
)

// PwdCommand is the /pwd command.
type PwdCommand struct{ h appcommand.SlashSession }

// New binds /pwd to a session host.
func New(h appcommand.SlashSession) dservice.SlashCommand { return &PwdCommand{h: h} }

func (c *PwdCommand) Name() string        { return "pwd" }
func (c *PwdCommand) Usage() string       { return "/pwd" }
func (c *PwdCommand) Description() string { return "print the workspace path" }

func (c *PwdCommand) Execute(_ context.Context, _ string, emitter dservice.CommandEmitter, args ...string) error {
	raw := ""
	if len(args) > 0 {
		raw = args[0]
	}
	if strings.TrimSpace(raw) != "" {
		return errors.New("usage: /pwd")
	}
	c.h.Write(emitter, "%s\n", c.h.Path())
	return nil
}
