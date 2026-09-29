package cmdnew

import (
	"context"
	"errors"
	"strings"

	appcommand "github.com/jahrulnr/nusallm/application/command"
	appgateway "github.com/jahrulnr/nusallm/application/gateway"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
)

// NewCommand is the /new command.
type NewCommand struct{ h appcommand.SlashSession }

// New binds /new to a session host.
func New(h appcommand.SlashSession) dservice.SlashCommand { return &NewCommand{h: h} }

func (c *NewCommand) Name() string        { return "new" }
func (c *NewCommand) Usage() string       { return "/new" }
func (c *NewCommand) Description() string { return "start a fresh conversation" }

func (c *NewCommand) Execute(ctx context.Context, _ string, emitter dservice.CommandEmitter, args ...string) error {
	raw := ""
	if len(args) > 0 {
		raw = args[0]
	}
	if strings.TrimSpace(raw) != "" {
		return errors.New("usage: /new")
	}
	if c.h.ConversationID() != "" {
		info, err := c.h.ConversationInfo(ctx, c.h.ConversationID())
		if err != nil {
			return err
		}
		if c.h.Route() == "" {
			c.h.SetRoute(info.Route)
		}
		if c.h.ModelID() == "" {
			if c.h.Route() == info.Route {
				c.h.SetModelID(info.ModelID)
			} else if gateway, err := c.h.GetGateway(ctx, c.h.Route()); err == nil && !gateway.Broken && appgateway.ModelCatalogContains(gateway.Models, info.ModelID) {
				c.h.SetModelID(info.ModelID)
			}
		}
	}
	c.h.SetConversationID("")
	c.h.Write(emitter, "new conversation; route=%q model=%q\n", c.h.Route(), c.h.ModelID())
	return nil
}
