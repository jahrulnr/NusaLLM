package cmdstatus

import (
	"context"
	"errors"
	"strings"
	"time"

	appcommand "github.com/jahrulnr/llm-base/application/command"
	dservice "github.com/jahrulnr/llm-base/infrastructure/domain/service"
)

// StatusCommand is the /status command.
type StatusCommand struct{ h appcommand.SlashSession }

// New binds /status to a session host.
func New(h appcommand.SlashSession) dservice.SlashCommand { return &StatusCommand{h: h} }

func (c *StatusCommand) Name() string        { return "status" }
func (c *StatusCommand) Usage() string       { return "/status" }
func (c *StatusCommand) Description() string { return "show the session state" }

func (c *StatusCommand) Execute(ctx context.Context, _ string, emitter dservice.CommandEmitter, args ...string) error {
	raw := ""
	if len(args) > 0 {
		raw = args[0]
	}
	if strings.TrimSpace(raw) != "" {
		return errors.New("usage: /status")
	}
	route, modelID := c.h.Route(), c.h.ModelID()
	if c.h.ConversationID() == "" {
		c.h.Write(emitter, "no active conversation\n")
	} else {
		info, err := c.h.ConversationInfo(ctx, c.h.ConversationID())
		if err != nil {
			return err
		}
		if route == "" {
			route = info.Route
		}
		if modelID == "" {
			modelID = info.ModelID
		}
		c.h.Write(emitter, "conversation %s\n", info.ID)
		if info.Title != "" {
			c.h.Write(emitter, "title       %q\n", info.Title)
		}
		c.h.Write(emitter, "messages    %d active", info.ActiveCount)
		if info.SealedSegments > 0 {
			c.h.Write(emitter, " (%d total, %d sealed segments)", info.MessageCount, info.SealedSegments)
		}
		c.h.Write(emitter, "\ncreated     %s\nupdated     %s\n",
			info.CreatedAt.Local().Format(time.RFC3339), info.UpdatedAt.Local().Format(time.RFC3339))
	}
	c.h.Write(emitter, "route       %s\nmodel       %s\n", route, modelID)
	if n := len(c.h.Attachments()); n > 0 {
		c.h.Write(emitter, "attachments %d pending\n", n)
	}
	c.h.Write(emitter, "workspace   %s\n", c.h.Path())
	return nil
}
