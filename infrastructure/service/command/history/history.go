package cmdhistory

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"unicode"

	appcommand "github.com/jahrulnr/llm-base/application/command"
	dservice "github.com/jahrulnr/llm-base/infrastructure/domain/service"
	commandinternal "github.com/jahrulnr/llm-base/infrastructure/service/command/internal"
)

// HistoryCommand is the /history command.
type HistoryCommand struct{ h appcommand.SlashSession }

// New binds /history to a session host.
func New(h appcommand.SlashSession) dservice.SlashCommand { return &HistoryCommand{h: h} }

func (c *HistoryCommand) Name() string        { return "history" }
func (c *HistoryCommand) Usage() string       { return "/history [count]" }
func (c *HistoryCommand) Description() string { return "show recent messages as chat" }

func (c *HistoryCommand) Execute(ctx context.Context, _ string, emitter dservice.CommandEmitter, args ...string) error {
	raw := ""
	if len(args) > 0 {
		raw = args[0]
	}
	if c.h.ConversationID() == "" {
		return errors.New("no active conversation; use /resume <id>")
	}
	limit := commandinternal.DefaultHistoryLimit
	if trimmed := strings.TrimSpace(raw); trimmed != "" {
		if strings.IndexFunc(trimmed, unicode.IsSpace) >= 0 {
			return errors.New("usage: /history [count]")
		}
		parsed, err := strconv.Atoi(trimmed)
		if err != nil || parsed < 1 || parsed > 200 {
			return errors.New("history count must be between 1 and 200")
		}
		limit = parsed
	}
	messages, err := c.h.ConversationHistory(ctx, c.h.ConversationID(), -limit, 0)
	if err != nil {
		return err
	}
	commandinternal.Write(c.h, emitter, messages)
	return nil
}
