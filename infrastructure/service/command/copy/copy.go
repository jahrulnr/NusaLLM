package cmdcopy

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/atotto/clipboard"
	appcommand "github.com/jahrulnr/nusallm/application/command"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
)

// CopyCommand is the /copy command.
type CopyCommand struct{ h appcommand.SlashSession }

// New binds /copy to a session host.
func New(h appcommand.SlashSession) dservice.SlashCommand { return &CopyCommand{h: h} }

func (c *CopyCommand) Name() string        { return "copy" }
func (c *CopyCommand) Usage() string       { return "/copy" }
func (c *CopyCommand) Description() string { return "copy the last answer to the clipboard" }

func (c *CopyCommand) Execute(ctx context.Context, _ string, emitter dservice.CommandEmitter, args ...string) error {
	raw := ""
	if len(args) > 0 {
		raw = args[0]
	}
	if strings.TrimSpace(raw) != "" {
		return errors.New("usage: /copy")
	}
	if c.h.ConversationID() == "" {
		return errors.New("no active conversation; use /resume <id>")
	}
	messages, err := c.h.ConversationHistory(ctx, c.h.ConversationID(), -50, 0)
	if err != nil {
		return err
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if content := messages[i].Content(); messages[i].Role() == enum.RoleAssistant && content != "" {
			if err := clipboard.WriteAll(content); err != nil {
				return fmt.Errorf("clipboard unavailable: %w", err)
			}
			c.h.Write(emitter, "copied %d bytes from %s\n", len(content), messages[i].ID())
			return nil
		}
	}
	return errors.New("no assistant answer to copy")
}
