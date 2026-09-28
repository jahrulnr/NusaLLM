package toolfile

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/service"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

// FilePatchTool rewrites a text file under the turn's workspace by
// literal search/replace — deliberately not unified diff: diff grammars
// are fragile for models, and exact-match anchoring either matches or
// fails, forcing a re-read instead of a fuzzy guess.
type FilePatchTool struct {
	dir    string
	schema valueobject.ToolSchema
}

// NewFilePatchTool binds the tool's default root: every requested path
// resolves inside it — os.Root confines traversal and symlink escapes
// alike. A turn-scoped workspace dir overrides it per call.
func NewFilePatchTool(root string) (service.Tool, error) {
	abs, err := checkDir(root)
	if err != nil {
		return nil, err
	}
	schema, err := valueobject.NewToolSchema(
		"apply_patch",
		"Replace an exact string in a UTF-8 text file relative to the workspace root.",
		json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Path relative to the workspace root"},"old_string":{"type":"string","description":"Exact text to find; may span multiple lines"},"new_string":{"type":"string","description":"Replacement text; empty deletes old_string"},"replace_all":{"type":"boolean","description":"Replace every occurrence instead of requiring a unique match (default false)"}},"required":["path","old_string","new_string"]}`),
		true,
		enum.ToolPermissionAgent,
	)
	if err != nil {
		return nil, err
	}
	return &FilePatchTool{dir: abs, schema: schema}, nil
}

func (tool *FilePatchTool) Schema() valueobject.ToolSchema {
	return tool.schema
}

func (tool *FilePatchTool) Execute(ctx context.Context, call entity.ToolCall, _ service.ToolEmitter) (service.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return service.ToolResult{}, err
	}
	var input struct {
		Path       string `json:"path"`
		OldString  string `json:"old_string"`
		NewString  string `json:"new_string"`
		ReplaceAll bool   `json:"replace_all"`
	}
	if err := json.Unmarshal([]byte(call.Input()), &input); err != nil {
		return service.ToolResult{}, fmt.Errorf("invalid apply_patch input: %w", err)
	}
	// An empty anchor would match between every byte — nonsense, not a delete.
	if input.OldString == "" {
		return service.ToolResult{}, fmt.Errorf("old_string must not be empty")
	}
	root, _, err := callRoot(ctx, tool.dir)
	if err != nil {
		return service.ToolResult{}, err
	}
	defer root.Close()
	info, err := root.Stat(input.Path)
	if err != nil {
		return service.ToolResult{}, err
	}
	if info.IsDir() {
		return service.ToolResult{}, fmt.Errorf("%q is a directory", input.Path)
	}
	// Patching loads the whole file into memory; files larger than 4 MiB
	// belong to exec/sed, not an in-memory edit tool.
	if info.Size() > 4<<20 {
		return service.ToolResult{}, fmt.Errorf("%q is too large to patch (%d bytes > 4 MiB)", input.Path, info.Size())
	}
	data, err := root.ReadFile(input.Path)
	if err != nil {
		return service.ToolResult{}, err
	}
	content := string(data)
	count := strings.Count(content, input.OldString)
	if count == 0 {
		// No fuzzy matching — the model must re-read and anchor on
		// text that is actually there.
		return service.ToolResult{}, fmt.Errorf("old_string not found in %q", input.Path)
	}
	if count > 1 && !input.ReplaceAll {
		return service.ToolResult{}, fmt.Errorf("old_string matches %d places in %q; add surrounding context or set replace_all", count, input.Path)
	}
	if input.ReplaceAll {
		content = strings.ReplaceAll(content, input.OldString, input.NewString)
	} else {
		content = strings.Replace(content, input.OldString, input.NewString, 1)
	}
	// Write through the root with the recorded mode — WriteFile only
	// applies perm on create, so an existing file keeps its bits anyway.
	if err := root.WriteFile(input.Path, []byte(content), info.Mode().Perm()); err != nil {
		return service.ToolResult{}, err
	}
	occurrence := "occurrences"
	if count == 1 {
		occurrence = "occurrence"
	}
	return service.ToolResult{Text: fmt.Sprintf("patched %s (%d %s)", input.Path, count, occurrence)}, nil
}
