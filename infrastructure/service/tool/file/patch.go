package toolfile

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	"github.com/jahrulnr/nusallm/infrastructure/domain/service"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

// FilePatchTool rewrites a text file named by a path argument by literal
// search/replace — deliberately not unified diff: diff grammars are fragile
// for models, and exact-match anchoring either matches or fails, forcing a
// re-read instead of a fuzzy guess. The workspace repository decides what
// the argument means, so the tool only reads and rewrites the placement.
type FilePatchTool struct {
	dir    string
	store  drepo.Workspace
	schema valueobject.ToolSchema
}

// NewFilePatchTool binds the tool's default root: a relative path resolves
// inside it, an absolute path inside it resolves relative to it. A
// turn-scoped workspace dir overrides it per call.
func NewFilePatchTool(store drepo.Workspace, root string) (service.Tool, error) {
	abs, err := checkDir(store, root)
	if err != nil {
		return nil, err
	}
	schema, err := valueobject.NewToolSchema(
		"apply_patch",
		"Replace an exact string in a UTF-8 text file relative to the workspace root. The path is expanded like the other file tools; a path outside the workspace is refused.",
		json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Path relative to the workspace root (or an absolute path inside it)"},"old_string":{"type":"string","description":"Exact text to find; may span multiple lines"},"new_string":{"type":"string","description":"Replacement text; empty deletes old_string"},"replace_all":{"type":"boolean","description":"Replace every occurrence instead of requiring a unique match (default false)"}},"required":["path","old_string","new_string"]}`),
		true,
		enum.ToolPermissionAgent,
	)
	if err != nil {
		return nil, err
	}
	return &FilePatchTool{dir: abs, store: store, schema: schema}, nil
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
	place, err := tool.store.LocateWrite(ctx, callDir(ctx, tool.dir), input.Path)
	if err != nil {
		return service.ToolResult{}, err
	}
	defer place.Close()
	f, info, err := place.Open(ctx)
	if err != nil {
		return service.ToolResult{}, err
	}
	defer f.Close()
	// Patching loads the whole file into memory; files larger than 4 MiB
	// belong to exec/sed, not an in-memory edit tool.
	if info.Size() > 4<<20 {
		return service.ToolResult{}, fmt.Errorf("%q is too large to patch (%d bytes > 4 MiB)", input.Path, info.Size())
	}
	data, err := io.ReadAll(io.LimitReader(f, (4<<20)+1))
	if err != nil {
		return service.ToolResult{}, err
	}
	if len(data) > 4<<20 {
		return service.ToolResult{}, fmt.Errorf("%q is too large to patch (> 4 MiB)", input.Path)
	}
	if err := f.Close(); err != nil {
		return service.ToolResult{}, err
	}
	content := string(data)
	// A model writes LF; a file written on Windows carries CRLF. The anchor
	// is the same text either way, so a miss retries with the file's own
	// line ending before giving up — the replacement keeps it too, so the
	// patch never rewrites a whole file's endings.
	anchor, replacement := input.OldString, input.NewString
	count := strings.Count(content, anchor)
	if count == 0 && strings.Contains(content, "\r\n") && !strings.Contains(anchor, "\r\n") {
		anchor = strings.ReplaceAll(input.OldString, "\n", "\r\n")
		replacement = strings.ReplaceAll(input.NewString, "\n", "\r\n")
		count = strings.Count(content, anchor)
	}
	if count == 0 {
		// No fuzzy matching — the model must re-read and anchor on
		// text that is actually there.
		return service.ToolResult{}, fmt.Errorf("old_string not found in %q", input.Path)
	}
	if count > 1 && !input.ReplaceAll {
		return service.ToolResult{}, fmt.Errorf("old_string matches %d places in %q; add surrounding context or set replace_all", count, input.Path)
	}
	if input.ReplaceAll {
		content = strings.ReplaceAll(content, anchor, replacement)
	} else {
		content = strings.Replace(content, anchor, replacement, 1)
	}
	// Write through the placement with the recorded mode — Create applies
	// its perm only on create, so an existing file keeps its bits anyway.
	if err := place.Create([]byte(content)); err != nil {
		return service.ToolResult{}, err
	}
	occurrence := "occurrences"
	if count == 1 {
		occurrence = "occurrence"
	}
	return service.ToolResult{Text: fmt.Sprintf("patched %s (%d %s)", input.Path, count, occurrence)}, nil
}
