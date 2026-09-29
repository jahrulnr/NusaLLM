// Package tool is the WebUI tool-catalog boundary — the registered tool
// schemas as metadata for display. It makes no claim about which tools
// are offered to the model.
package tool

import (
	"context"
	"encoding/json"

	"github.com/gin-gonic/gin"

	"github.com/jahrulnr/nusallm/infrastructure/delivery/http/internal/transport"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

// Catalog is the read surface — the registered tool schemas.
// *appconversation.ConversationApp satisfies it.
type Catalog interface {
	ToolSchemas(context.Context, string) ([]valueobject.ToolSchema, error)
}

// Deps wires the use case; Tools is optional — nil leaves the route
// unregistered.
type Deps struct {
	Tools Catalog
}

// Register mounts this boundary's routes under /api.
func Register(api *gin.RouterGroup, d Deps) {
	s := &server{tools: d.Tools}
	if d.Tools != nil {
		api.GET("/tools", s.list)
	}
}

type server struct {
	tools Catalog
}

// toolDTO is the registered catalog entry — the schema text is catalog
// data authored with the tool, not untrusted runtime output, but it is
// still untrusted display text to the client.
type toolDTO struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Enabled     bool            `json:"enabled"`
	Permission  string          `json:"permission"`
}

func toTool(s valueobject.ToolSchema) toolDTO {
	return toolDTO{
		Name:        s.Name(),
		Description: s.Description(),
		Parameters:  s.Parameters(),
		Enabled:     s.Enabled(),
		Permission:  s.Permission().String(),
	}
}

func (s *server) list(c *gin.Context) {
	out := make([]toolDTO, 0)
	schemas, err := s.tools.ToolSchemas(c.Request.Context(), c.Query("conversationId"))
	if err != nil {
		transport.RespondError(c, err)
		return
	}
	for _, sc := range schemas {
		out = append(out, toTool(sc))
	}
	c.JSON(200, struct {
		Data []toolDTO `json:"data"`
	}{Data: out})
}
