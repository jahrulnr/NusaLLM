package toolfile

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	"github.com/jahrulnr/nusallm/infrastructure/domain/service"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

// FileWriteTool creates or replaces a text file named by a path argument —
// the workspace repository decides what the argument means and refuses a
// write outside the workspace, so this adapter never expands, joins, or
// stats a path.
type FileWriteTool struct {
	dir    string
	store  drepo.Workspace
	schema valueobject.ToolSchema
}

// NewFileWriteTool binds the tool's default root: a relative path resolves
// inside it, an absolute path inside it resolves relative to it. A
// turn-scoped workspace dir overrides it per call.
func NewFileWriteTool(store drepo.Workspace, root string) (service.Tool, error) {
	abs, err := checkDir(store, root)
	if err != nil {
		return nil, err
	}
	schema, err := valueobject.NewToolSchema(
		"write_file",
		"Create or overwrite a UTF-8 text file relative to the workspace root. A leading ~/… or a file: URL is expanded, but a path outside the workspace is refused — bind the folder or use exec.",
		json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Path relative to the workspace root (or an absolute path inside it)"},"content":{"type":"string","description":"Complete file content — an existing file is replaced"}},"required":["path","content"]}`),
		true,
		enum.ToolPermissionAgent,
	)
	if err != nil {
		return nil, err
	}
	return &FileWriteTool{dir: abs, store: store, schema: schema}, nil
}

func (tool *FileWriteTool) Schema() valueobject.ToolSchema {
	return tool.schema
}

func (tool *FileWriteTool) Execute(ctx context.Context, call entity.ToolCall, _ service.ToolEmitter) (service.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return service.ToolResult{}, err
	}
	var input struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(call.Input()), &input); err != nil {
		return service.ToolResult{}, fmt.Errorf("invalid write_file input: %w", err)
	}
	if strings.TrimSpace(input.Path) == "" {
		return service.ToolResult{}, fmt.Errorf("path is required")
	}
	place, err := tool.store.LocateWrite(ctx, callDir(ctx, tool.dir), input.Path)
	if err != nil {
		return service.ToolResult{}, err
	}
	defer place.Close()
	// 0o600: agent-written files may carry secrets (.env, tokens) — the
	// same convention as repository writes that can hold key material. The
	// perm applies only at creation; an existing file keeps its mode.
	// Parent directories are created owner-only for the same reason.
	if err := place.Create([]byte(input.Content)); err != nil {
		return service.ToolResult{}, err
	}
	// Confirm, don't echo: the returned string is what the call's output
	// budget caps, and replaying content would double the evidence noise.
	return service.ToolResult{Text: fmt.Sprintf("wrote %d bytes to %s", len(input.Content), input.Path)}, nil
}
