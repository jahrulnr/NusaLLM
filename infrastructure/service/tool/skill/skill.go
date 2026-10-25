// Package skill provides automatic skill discovery and on-demand source reads.
package skill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	skillassets "github.com/jahrulnr/nusallm/assets/skills"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

// Builtins is the shipped skill tree, copied by the repository at bootstrap.
func Builtins() fs.FS {
	return skillassets.Builtins()
}

type SkillsTool struct {
	store  drepo.Skills
	schema valueobject.ToolSchema
}
type ReadSkillTool struct {
	store  drepo.Skills
	schema valueobject.ToolSchema
}

func NewSkillsTool(store drepo.Skills) (dservice.Tool, error) {
	if store == nil {
		return nil, errors.New("skills repository is required")
	}
	schema, err := valueobject.NewToolSchema("skills", "Reports the available skills for the bound workspace. Automatically hydrated before provider calls; callable by the agent to refresh the catalog.", json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Optional workspace context; execution uses the workspace bound by the harness"}}}`), true, enum.ToolPermissionGeneral)
	if err != nil {
		return nil, err
	}
	return &SkillsTool{store: store, schema: schema}, nil
}

func NewReadSkillTool(store drepo.Skills) (dservice.Tool, error) {
	if store == nil {
		return nil, errors.New("skills repository is required")
	}
	schema, err := valueobject.NewToolSchema("read_skill", "Read the complete SKILL.md of an available skill by name. The hydrated skills catalog lists names, descriptions and paths. Resolve supporting files relative to the returned skill directory.", json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","description":"Skill name from the hydrated catalog"}},"required":["name"]}`), true, enum.ToolPermissionAgent)
	if err != nil {
		return nil, err
	}
	return &ReadSkillTool{store: store, schema: schema}, nil
}

func (tool *SkillsTool) Schema() valueobject.ToolSchema    { return tool.schema }
func (tool *ReadSkillTool) Schema() valueobject.ToolSchema { return tool.schema }

func (tool *SkillsTool) Execute(ctx context.Context, _ entity.ToolCall, _ dservice.ToolEmitter) (dservice.ToolResult, error) {
	catalog, err := tool.store.List(ctx, dservice.WorkspaceDir(ctx))
	if err != nil {
		return dservice.ToolResult{}, err
	}
	raw, err := json.MarshalIndent(catalog, "", "  ")
	if err != nil {
		return dservice.ToolResult{}, err
	}
	return dservice.ToolResult{Text: string(raw)}, nil
}

func (tool *ReadSkillTool) Execute(ctx context.Context, call entity.ToolCall, _ dservice.ToolEmitter) (dservice.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return dservice.ToolResult{}, err
	}
	var input struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(call.Input()), &input); err != nil {
		return dservice.ToolResult{}, fmt.Errorf("invalid read_skill input: %w", err)
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		return dservice.ToolResult{}, errors.New("name is required")
	}
	skill, text, err := tool.store.Read(ctx, dservice.WorkspaceDir(ctx), input.Name)
	if err != nil {
		return dservice.ToolResult{}, err
	}
	return dservice.ToolResult{Text: fmt.Sprintf("Skill: %s\nPath: %s\nDirectory: %s\n\n%s", skill.Name, skill.Path, filepath.Dir(skill.Path), text)}, nil
}
