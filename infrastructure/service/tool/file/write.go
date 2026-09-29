package toolfile

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	"github.com/jahrulnr/nusallm/infrastructure/domain/service"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

// FileWriteTool creates or replaces a text file under the turn's
// workspace — the constructor carries the schema and validates the
// default sandbox boundary; Execute resolves the call input against an
// os.Root opened per call so `..` traversal and symlink escapes fail
// alike.
type FileWriteTool struct {
	dir    string
	store  drepo.Workspace
	schema valueobject.ToolSchema
}

// NewFileWriteTool binds the tool's default root: every requested path
// resolves inside it — os.Root confines traversal and symlink escapes
// alike. A turn-scoped workspace dir overrides it per call.
func NewFileWriteTool(store drepo.Workspace, root string) (service.Tool, error) {
	abs, err := checkDir(store, root)
	if err != nil {
		return nil, err
	}
	schema, err := valueobject.NewToolSchema(
		"write_file",
		"Create or overwrite a UTF-8 text file relative to the workspace root.",
		json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Path relative to the workspace root"},"content":{"type":"string","description":"Complete file content — an existing file is replaced"}},"required":["path","content"]}`),
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
	root, err := callRoot(ctx, tool.store, tool.dir)
	if err != nil {
		return service.ToolResult{}, err
	}
	defer root.Close()
	// Stat first so an existing directory is reported as such instead of
	// surfacing an opaque open error; a missing path is the normal case.
	if info, err := root.Stat(input.Path); err == nil && info.IsDir() {
		return service.ToolResult{}, fmt.Errorf("%q is a directory", input.Path)
	}
	if dir := filepath.Dir(input.Path); dir != "." {
		// 0o700: created dirs may hold agent-written secrets, matching the
		// repository convention for key-bearing content.
		if err := root.MkdirAll(dir, 0o700); err != nil {
			return service.ToolResult{}, err
		}
	}
	// 0o600: agent-written files may carry secrets (.env, tokens) — the
	// same convention as repository writes that can hold key material. The
	// perm applies only at creation; an existing file keeps its mode.
	if err := root.WriteFile(input.Path, []byte(input.Content), 0o600); err != nil {
		return service.ToolResult{}, err
	}
	// Confirm, don't echo: the returned string is what the call's output
	// budget caps, and replaying content would double the evidence noise.
	return service.ToolResult{Text: fmt.Sprintf("wrote %d bytes to %s", len(input.Content), input.Path)}, nil
}
