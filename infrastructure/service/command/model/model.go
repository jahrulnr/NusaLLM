package cmdmodel

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"

	appcommand "github.com/jahrulnr/nusallm/application/command"
	"github.com/jahrulnr/nusallm/infrastructure/domain/service"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
)

// ModelCommand is the /model command.
type ModelCommand struct{ h appcommand.SlashSession }

// New binds /model to a session host.
func New(h appcommand.SlashSession) dservice.SlashCommand { return &ModelCommand{h: h} }

func (c *ModelCommand) Name() string        { return "model" }
func (c *ModelCommand) Usage() string       { return "/model [model-id]" }
func (c *ModelCommand) Description() string { return "list or select a model on the route" }

func (c *ModelCommand) Complete(ctx context.Context, arg string) []dservice.CommandCompletion {
	if strings.IndexFunc(arg, unicode.IsSpace) >= 0 {
		return nil
	}
	route := c.h.Route()
	if route == "" && c.h.ConversationID() != "" {
		info, err := c.h.ConversationInfo(ctx, c.h.ConversationID())
		if err == nil {
			route = info.Route
		}
	}
	if route == "" {
		return nil
	}
	gateway, err := c.h.GetGateway(ctx, route)
	if err != nil {
		return nil
	}
	arg = strings.ToLower(arg)
	var rows []service.CommandCompletion
	for _, model := range gateway.ListedModels() {
		id := model.ID()
		if arg != "" && !strings.Contains(strings.ToLower(id), arg) {
			continue
		}
		desc := "on " + gateway.ID
		if id == c.h.ModelID() {
			desc = "* " + desc
		}
		rows = append(rows, service.CommandCompletion{Usage: "/model " + id, Desc: desc})
	}
	if len(rows) == 0 {
		return nil
	}
	return rows
}

func (c *ModelCommand) Execute(ctx context.Context, _ string, emitter dservice.CommandEmitter, args ...string) error {
	raw := ""
	if len(args) > 0 {
		raw = args[0]
	}
	route, selected := c.h.Route(), c.h.ModelID()
	if c.h.ConversationID() != "" && (route == "" || selected == "") {
		info, err := c.h.ConversationInfo(ctx, c.h.ConversationID())
		if err != nil {
			return err
		}
		if route == "" {
			route = info.Route
		}
		if selected == "" {
			selected = info.ModelID
		}
	}
	if route == "" {
		return errors.New("select a route with /route <gateway-id> first")
	}
	gateway, err := c.h.GetGateway(ctx, route)
	if err != nil {
		return err
	}
	if gateway.Broken {
		return fmt.Errorf("gateway %q is broken and cannot be used", route)
	}
	id := strings.TrimSpace(raw)
	if id == "" {
		listed := gateway.ListedModels()
		if len(listed) == 0 {
			c.h.Write(emitter, "gateway %s has no listed models\n", route)
			return nil
		}
		for _, m := range listed {
			marker := " "
			if m.ID() == selected {
				marker = "*"
			}
			c.h.Write(emitter, "%s %s\n", marker, m.ID())
		}
		return nil
	}
	if strings.IndexFunc(id, unicode.IsSpace) >= 0 {
		return errors.New("usage: /model [model-id]")
	}
	if !gateway.CatalogContains(id) {
		return fmt.Errorf("model %q is not in gateway %q; use /model to list", id, route)
	}
	if !gateway.ModelListed(id) {
		return fmt.Errorf("model %q is not listed (context below %d tokens)", id, appcommand.MinListedContextWindow)
	}
	c.h.SetModelID(id)
	c.h.Write(emitter, "model set to %s on %s\n", id, route)
	return nil
}
