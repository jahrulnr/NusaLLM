package cmddelete

import (
	"context"
	"errors"
	"strings"
	"unicode"

	appcommand "github.com/jahrulnr/nusallm/application/command"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	commandinternal "github.com/jahrulnr/nusallm/infrastructure/service/command/internal"
)

// DeleteCommand is the /delete command.
type DeleteCommand struct{ h appcommand.SlashSession }

// New binds /delete to a session host.
func New(h appcommand.SlashSession) dservice.SlashCommand { return &DeleteCommand{h: h} }

func (c *DeleteCommand) Name() string        { return "delete" }
func (c *DeleteCommand) Usage() string       { return "/delete [id]" }
func (c *DeleteCommand) Description() string { return "pick or delete a conversation" }

func (c *DeleteCommand) Complete(ctx context.Context, arg string) []dservice.CommandCompletion {
	return commandinternal.Conversations(c.h, "delete")(ctx, arg)
}

func (c *DeleteCommand) Execute(ctx context.Context, _ string, emitter dservice.CommandEmitter, args ...string) error {
	raw := ""
	if len(args) > 0 {
		raw = args[0]
	}
	id := strings.TrimSpace(raw)
	if strings.IndexFunc(id, unicode.IsSpace) >= 0 {
		return errors.New("usage: /delete [conversation-id]")
	}
	if id == "" {
		id = c.h.ConversationID()
	}
	if id == "" {
		return errors.New("no active conversation; /delete <id> or /resume first")
	}
	if err := c.h.DeleteConversation(ctx, id); err != nil {
		return err
	}
	if id == c.h.ConversationID() {
		c.h.SetConversationID("")
	}
	c.h.Write(emitter, "deleted %s\n", id)
	return nil
}
