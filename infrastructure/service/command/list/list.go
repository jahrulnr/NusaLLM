package cmdlist

import (
	"context"
	"errors"
	"strings"
	"time"

	appcommand "github.com/jahrulnr/llm-base/application/command"
	dservice "github.com/jahrulnr/llm-base/infrastructure/domain/service"
	commandinternal "github.com/jahrulnr/llm-base/infrastructure/service/command/internal"
)

// ListCommand is the /list command.
type ListCommand struct{ h appcommand.SlashSession }

// New binds /list to a session host.
func New(h appcommand.SlashSession) dservice.SlashCommand { return &ListCommand{h: h} }

func (c *ListCommand) Name() string        { return "list" }
func (c *ListCommand) Usage() string       { return "/list" }
func (c *ListCommand) Description() string { return "list saved conversations" }

func (c *ListCommand) Execute(ctx context.Context, _ string, emitter dservice.CommandEmitter, args ...string) error {
	raw := ""
	if len(args) > 0 {
		raw = args[0]
	}
	if strings.TrimSpace(raw) != "" {
		return errors.New("usage: /list")
	}
	conversations, err := c.h.ListConversations(ctx)
	if err != nil {
		return err
	}
	if len(conversations) == 0 {
		c.h.Write(emitter, "no saved conversations\n")
		return nil
	}
	titles := commandinternal.DisplayTitles(ctx, c.h, conversations)
	for _, conv := range conversations {
		marker := " "
		if conv.ID == c.h.ConversationID() {
			marker = "*"
		}
		c.h.Write(emitter, "%s %s route=%s model=%s messages=%d", marker, conv.ID, conv.Route, conv.ModelID, conv.MessageCount)
		if title := titles[conv.ID]; title != "untitled" {
			c.h.Write(emitter, " title=%q", title)
		}
		if !conv.UpdatedAt.IsZero() {
			c.h.Write(emitter, " updated=%s", conv.UpdatedAt.Local().Format(time.RFC3339))
		}
		c.h.Write(emitter, "\n")
	}
	return nil
}
