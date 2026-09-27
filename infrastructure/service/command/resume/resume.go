package cmdresume

import (
	"context"
	"errors"
	"strings"
	"unicode"

	appcommand "github.com/jahrulnr/llm-base/application/command"
	dservice "github.com/jahrulnr/llm-base/infrastructure/domain/service"
	commandinternal "github.com/jahrulnr/llm-base/infrastructure/service/command/internal"
)

// ResumeCommand is the /resume command.
type ResumeCommand struct{ h appcommand.SlashSession }

// New binds /resume to a session host.
func New(h appcommand.SlashSession) dservice.SlashCommand { return &ResumeCommand{h: h} }

func (c *ResumeCommand) Name() string  { return "resume" }
func (c *ResumeCommand) Usage() string { return "/resume [id]" }
func (c *ResumeCommand) Description() string {
	return "resume a saved conversation and show recent messages"
}

func (c *ResumeCommand) Complete(ctx context.Context, arg string) []dservice.CommandCompletion {
	return commandinternal.Conversations(c.h, "resume")(ctx, arg)
}

func (c *ResumeCommand) Execute(ctx context.Context, _ string, emitter dservice.CommandEmitter, args ...string) error {
	raw := ""
	if len(args) > 0 {
		raw = args[0]
	}
	id := strings.TrimSpace(raw)
	if id == "" {
		if conversations, err := c.h.ListConversations(ctx); err != nil {
			return err
		} else if len(conversations) == 0 {
			return errors.New("no saved conversations")
		}
		return errors.New("usage: /resume <conversation-id>")
	}
	if strings.IndexFunc(id, unicode.IsSpace) >= 0 {
		return errors.New("usage: /resume <conversation-id>")
	}
	conv, err := c.h.ConversationInfo(ctx, id)
	if err != nil {
		return err
	}
	messages, err := c.h.ConversationHistory(ctx, id, -commandinternal.DefaultHistoryLimit, 0)
	if err != nil {
		return err
	}
	c.h.SetConversationID(id)
	c.h.SetRoute("")
	c.h.SetModelID("")
	c.h.Write(emitter, "resumed %s (route=%s model=%s)\n", id, conv.Route, conv.ModelID)
	commandinternal.Write(c.h, emitter, messages)
	return nil
}
