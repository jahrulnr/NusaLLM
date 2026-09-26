package toolfile

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/jahrulnr/llm-base/infrastructure/domain/entity"
	"github.com/jahrulnr/llm-base/infrastructure/domain/enum"
	"github.com/jahrulnr/llm-base/infrastructure/domain/service"
	"github.com/jahrulnr/llm-base/infrastructure/domain/valueobject"
)

// FileReadTool reads a text file under a fixed root — the constructor
// carries the schema and validates the sandbox boundary; Execute resolves
// the call input against an os.Root so symlink escapes fail too.
type FileReadTool struct {
	root   *os.Root
	schema valueobject.ToolSchema
}

// NewFileReadTool binds the tool to root: every requested path resolves
// inside it — os.Root confines traversal and symlink escapes alike.
func NewFileReadTool(root string) (service.Tool, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	schema, err := valueobject.NewToolSchema(
		"read_file",
		"Read a UTF-8 text file relative to the workspace root.",
		json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Path relative to the workspace root"}},"required":["path"]}`),
		true,
		enum.ToolPermissionAgent,
	)
	if err != nil {
		_ = r.Close()
		return nil, err
	}
	return &FileReadTool{root: r, schema: schema}, nil
}

func (tool *FileReadTool) Schema() valueobject.ToolSchema {
	return tool.schema
}

func (tool *FileReadTool) Execute(ctx context.Context, call entity.ToolCall, _ service.ToolEmitter) (service.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return service.ToolResult{}, err
	}
	var input struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(call.Input()), &input); err != nil {
		return service.ToolResult{}, fmt.Errorf("invalid read_file input: %w", err)
	}
	// Reject oversized files before reading — the call's output budget is
	// the honest cap, enforced again by SetToolOutput on the way out.
	info, err := tool.root.Stat(input.Path)
	if err != nil {
		return service.ToolResult{}, err
	}
	if info.IsDir() {
		return service.ToolResult{}, fmt.Errorf("%q is a directory", input.Path)
	}
	if budget := int64(call.OutputBudget()); info.Size() > budget {
		return service.ToolResult{}, fmt.Errorf("%q exceeds output budget (%d > %d bytes)", input.Path, info.Size(), budget)
	}
	f, err := tool.root.Open(input.Path)
	if err != nil {
		return service.ToolResult{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, int64(call.OutputBudget())+1))
	if err != nil {
		return service.ToolResult{}, err
	}
	return service.ToolResult{Text: string(data)}, nil
}
