package cmddelete

import (
	"context"
	"errors"
	"strings"
	"unicode"

	appcommand "github.com/jahrulnr/nusallm/application/command"
	commandinternal "github.com/jahrulnr/nusallm/infrastructure/delivery/cli/commands/internal"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
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
		// The room may already be gone. For the active room the user's
		// intent is fulfilled — leaving the id bound traps every later
		// command on the same 404 — so a 404 there clears it like any
		// successful delete. Any other failure, and a 404 for a target
		// the user named explicitly, still reports.
		if apperror.As(err).StatusCode() != apperror.StatusNotFound || id != c.h.ConversationID() {
			return err
		}
	}
	if id == c.h.ConversationID() {
		c.h.SetConversationID("")
	}
	c.h.Write(emitter, "deleted %s\n", id)
	return nil
}
