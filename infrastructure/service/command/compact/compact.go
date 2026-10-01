package cmdcompact

import (
	"context"
	"errors"
	"strings"

	appcommand "github.com/jahrulnr/nusallm/application/command"
	"github.com/jahrulnr/nusallm/infrastructure/domain/event"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	svccommand "github.com/jahrulnr/nusallm/infrastructure/service/command"
)

// CompactCommand is the /compact command. The fold runs on the room's
// worker, so the command is worker-bound: Job validates the line and
// binds the closure, Registry.Job hands it to transports that run jobs
// asynchronously, and Execute runs the same closure inline.
type CompactCommand struct{ h appcommand.SlashSession }

// New binds /compact to a session host.
func New(h appcommand.SlashSession) dservice.SlashCommand { return &CompactCommand{h: h} }

func (c *CompactCommand) Name() string        { return "compact" }
func (c *CompactCommand) Usage() string       { return "/compact" }
func (c *CompactCommand) Description() string { return "fold history into a checkpoint now" }

// Job validates the arguments and binds the fold — the closure runs on
// the conversation worker and confirms through the emitter.
func (c *CompactCommand) Job(args ...string) (svccommand.JobFunc, error) {
	raw := ""
	if len(args) > 0 {
		raw = args[0]
	}
	if strings.TrimSpace(raw) != "" {
		return nil, errors.New("usage: /compact")
	}
	if c.h.ConversationID() == "" {
		return nil, errors.New("no active conversation; use /resume <id>")
	}
	convID := c.h.ConversationID()
	return func(ctx context.Context, emit dservice.CommandEmitter, _ event.TurnEventHandler) error {
		if err := c.h.CompactConversation(ctx, convID); err != nil {
			return err
		}
		c.h.Write(emit, "compacted %s\n", convID)
		return nil
	}, nil
}

func (c *CompactCommand) Execute(ctx context.Context, _ string, emitter dservice.CommandEmitter, args ...string) error {
	job, err := c.Job(args...)
	if err != nil {
		return err
	}
	return job(ctx, emitter, nil)
}
