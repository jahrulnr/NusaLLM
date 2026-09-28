package toolfile

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/service"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
	"github.com/jahrulnr/nusallm/infrastructure/service/wire"
)

// FileReadTool reads a text file under the turn's workspace — the
// constructor carries the schema and validates the default sandbox
// boundary; Execute resolves the call input against an os.Root opened
// per call so symlink escapes fail too.
type FileReadTool struct {
	dir    string
	schema valueobject.ToolSchema
}

// NewFileReadTool binds the tool's default root: every requested path
// resolves inside it — os.Root confines traversal and symlink escapes
// alike. A turn-scoped workspace dir overrides it per call.
func NewFileReadTool(root string) (service.Tool, error) {
	abs, err := checkDir(root)
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
		return nil, err
	}
	return &FileReadTool{dir: abs, schema: schema}, nil
}

func (tool *FileReadTool) Schema() valueobject.ToolSchema {
	return tool.schema
}

func (tool *FileReadTool) Execute(ctx context.Context, call entity.ToolCall, _ service.ToolEmitter) (result service.ToolResult, retErr error) {
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
	root, _, err := callRoot(ctx, tool.dir)
	if err != nil {
		return service.ToolResult{}, err
	}
	defer func() {
		if closeErr := root.Close(); retErr == nil {
			retErr = closeErr
		}
	}()
	f, info, err := wire.OpenRegularFile(root, input.Path)
	if err != nil {
		return service.ToolResult{}, err
	}
	defer func() {
		if closeErr := f.Close(); retErr == nil {
			retErr = closeErr
		}
	}()
	if budget := int64(call.OutputBudget()); info.Size() > budget {
		return service.ToolResult{}, fmt.Errorf("%q exceeds output budget (%d > %d bytes)", input.Path, info.Size(), budget)
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(call.OutputBudget())+1))
	if err != nil {
		return service.ToolResult{}, err
	}
	return service.ToolResult{Text: string(data)}, nil
}
