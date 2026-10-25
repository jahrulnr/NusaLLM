// Package custom advertises the executable paths supplied to terminal tools.
package custom

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

type Tool struct {
	environment dservice.CommandEnvironment
	schema      valueobject.ToolSchema
}

func NewTool(environment dservice.CommandEnvironment) (dservice.Tool, error) {
	if environment == nil {
		return nil, errors.New("command environment is required")
	}
	schema, err := valueobject.NewToolSchema("custom_tools", "Reports custom executable directories on the terminal PATH. Hydrated by the harness; use companion skills and exec to invoke tools.", json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Working directory bound by the harness"}},"required":["path"]}`), true, enum.ToolPermissionSystem)
	if err != nil {
		return nil, err
	}
	return &Tool{environment: environment, schema: schema}, nil
}

func (t *Tool) Schema() valueobject.ToolSchema { return t.schema }
func (t *Tool) Execute(ctx context.Context, _ entity.ToolCall, _ dservice.ToolEmitter) (dservice.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return dservice.ToolResult{}, err
	}
	raw, err := json.MarshalIndent(struct {
		Paths         []string `json:"paths"`
		PythonCommand string   `json:"python_command"`
	}{Paths: t.environment.ToolPaths(dservice.WorkspaceDir(ctx)), PythonCommand: "python3"}, "", "  ")
	return dservice.ToolResult{Text: string(raw)}, err
}
