package cmdroute

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"

	appcommand "github.com/jahrulnr/nusallm/application/command"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/service"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
)

// RouteCommand is the /route command.
type RouteCommand struct{ h appcommand.SlashSession }

// New binds /route to a session host.
func New(h appcommand.SlashSession) dservice.SlashCommand { return &RouteCommand{h: h} }

func (c *RouteCommand) Name() string        { return "route" }
func (c *RouteCommand) Usage() string       { return "/route [gateway-id]" }
func (c *RouteCommand) Description() string { return "list or select a gateway" }

func (c *RouteCommand) Complete(ctx context.Context, arg string) []dservice.CommandCompletion {
	if strings.IndexFunc(arg, unicode.IsSpace) >= 0 {
		return nil
	}
	gateways, err := c.h.ListGateways(ctx)
	if err != nil {
		return nil
	}
	arg = strings.ToLower(arg)
	var rows []service.CommandCompletion
	for _, gateway := range gateways {
		if arg != "" &&
			!strings.Contains(strings.ToLower(gateway.ID), arg) &&
			!strings.Contains(strings.ToLower(gateway.Name), arg) {
			continue
		}
		desc := fmt.Sprintf("%s · %s · %d models", gateway.Name, gateway.Style, len(gateway.Models))
		if !gateway.KeySet {
			desc += " · no-key"
		}
		if gateway.Broken {
			desc += " · broken"
		}
		if gateway.ID == c.h.Route() {
			desc = "* " + desc
		}
		rows = append(rows, service.CommandCompletion{Usage: "/route " + gateway.ID, Desc: desc})
	}
	if len(rows) == 0 {
		return nil
	}
	return rows
}

func (c *RouteCommand) Execute(ctx context.Context, _ string, emitter dservice.CommandEmitter, args ...string) error {
	raw := ""
	if len(args) > 0 {
		raw = args[0]
	}
	id := strings.TrimSpace(raw)
	if id == "" {
		gateways, err := c.h.ListGateways(ctx)
		if err != nil {
			return err
		}
		if len(gateways) == 0 {
			c.h.Write(emitter, "no configured gateways — add one: nusallm gateway add <id> --style <style> --endpoint <url> --models <m> --key-stdin\n")
			return nil
		}
		selected := c.h.Route()
		if selected == "" && c.h.ConversationID() != "" {
			info, err := c.h.ConversationInfo(ctx, c.h.ConversationID())
			if err != nil {
				return err
			}
			selected = info.Route
		}
		for _, gateway := range gateways {
			marker := " "
			if gateway.ID == selected {
				marker = "*"
			}
			key := "no-key"
			if gateway.KeySet {
				key = "key-set"
			}
			broken := ""
			if gateway.Broken {
				broken = " broken"
			}
			c.h.Write(emitter, "%s %s %s style=%s models=%d %s%s\n", marker, gateway.ID, gateway.Name, gateway.Style, len(gateway.Models), key, broken)
		}
		return nil
	}
	if strings.IndexFunc(id, unicode.IsSpace) >= 0 {
		return errors.New("usage: /route [gateway-id]")
	}
	gateway, err := c.h.GetGateway(ctx, id)
	if err != nil {
		return err
	}
	if gateway.Broken {
		return fmt.Errorf("gateway %q is broken and cannot be selected", id)
	}
	c.h.SetRoute(gateway.ID)
	if c.h.ModelID() != "" && !entity.ModelCatalogContains(gateway.Models, c.h.ModelID()) {
		c.h.SetModelID("")
	}
	c.h.Write(emitter, "route set to %s (%s)\n", gateway.ID, gateway.Name)
	return nil
}
