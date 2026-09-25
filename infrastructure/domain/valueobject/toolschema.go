package valueobject

import (
	"encoding/json"
	"errors"

	"github.com/jahrulnr/llm-base/infrastructure/domain/enum"
)

// ToolSchema describes a tool the agent may call. Adapters translate it into
// each provider's tool definition wire format. enabled toggles the schema;
// permission restricts who may invoke it (see CallableByAgent /
// CallableBySystem).
type ToolSchema struct {
	name        string
	description string
	parameters  json.RawMessage
	enabled     bool
	permission  enum.ToolPermission
}

func NewToolSchema(name, description string, parameters json.RawMessage, enabled bool, permission enum.ToolPermission) (ToolSchema, error) {
	s := ToolSchema{
		name:        name,
		description: description,
		parameters:  parameters,
		enabled:     enabled,
		permission:  permission,
	}
	return s, s.validate()
}

func (s ToolSchema) validate() error {
	if s.name == "" {
		return errors.New("name is required")
	}
	return nil
}

func (s ToolSchema) Name() string                { return s.name }
func (s ToolSchema) Description() string         { return s.description }
func (s ToolSchema) Parameters() json.RawMessage { return s.parameters }
func (s ToolSchema) Enabled() bool               { return s.enabled }
func (s ToolSchema) Permission() enum.ToolPermission {
	return s.permission
}

// CallableByAgent reports whether the schema may be offered to or invoked by
// the model. Disabled or unknown permission fails closed.
func (s ToolSchema) CallableByAgent() bool {
	return s.enabled && (s.permission == enum.ToolPermissionAgent || s.permission == enum.ToolPermissionGeneral)
}

// CallableBySystem reports whether the application itself may invoke it.
// Disabled or unknown permission fails closed.
func (s ToolSchema) CallableBySystem() bool {
	return s.enabled && (s.permission == enum.ToolPermissionSystem || s.permission == enum.ToolPermissionGeneral)
}
