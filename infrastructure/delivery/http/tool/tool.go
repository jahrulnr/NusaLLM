// Package tool is the WebUI tool-catalog boundary — the registered tool
// schemas as metadata for display. It makes no claim about which tools
// are offered to the model.
package tool

import (
	"encoding/json"

	"github.com/gin-gonic/gin"

	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

// Catalog is the read surface — the registered tool schemas.
// *appconversation.ConversationApp satisfies it.
type Catalog interface {
	ToolSchemas() []valueobject.ToolSchema
}

type Deps struct {
	Tools Catalog
}

// Register mounts this boundary's routes under /api.
func Register(api *gin.RouterGroup, d Deps) {
	s := &server{tools: d.Tools}
	api.GET("/tools", s.list)
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
	if s.tools != nil {
		for _, sc := range s.tools.ToolSchemas() {
			out = append(out, toTool(sc))
		}
	}
	c.JSON(200, struct {
		Data []toolDTO `json:"data"`
	}{Data: out})
}
