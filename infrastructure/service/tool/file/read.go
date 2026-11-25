package toolfile

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	"github.com/jahrulnr/nusallm/infrastructure/domain/service"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

// FileReadTool reads a text file named by a path argument — the workspace
// repository decides what the argument means (workspace-relative, an
// absolute path, a file: URL, ~/…), so this adapter never expands or joins
// it. The constructor carries the schema and validates the default sandbox
// boundary.
type FileReadTool struct {
	dir    string
	store  drepo.Workspace
	schema valueobject.ToolSchema
}

// NewFileReadTool binds the tool's default root: a relative path resolves
// inside it, an absolute path anchors where it lives. A turn-scoped
// workspace dir overrides it per call.
func NewFileReadTool(store drepo.Workspace, root string) (service.Tool, error) {
	abs, err := checkDir(store, root)
	if err != nil {
		return nil, err
	}
	schema, err := valueobject.NewToolSchema(
		"read_file",
		"Read a UTF-8 text file relative to the workspace root, an absolute path anywhere on this machine, or a ~/… path naming the owner's home.",
		json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Path relative to the workspace root, an absolute path anywhere on disk, or ~/… for the owner's home"}},"required":["path"]}`),
		true,
		enum.ToolPermissionAgent,
	)
	if err != nil {
		return nil, err
	}
	return &FileReadTool{dir: abs, store: store, schema: schema}, nil
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
	place, err := tool.store.LocateRead(ctx, callDir(ctx, tool.dir), input.Path)
	if err != nil {
		return service.ToolResult{}, err
	}
	defer func() {
		if closeErr := place.Close(); retErr == nil {
			retErr = closeErr
		}
	}()
	f, info, err := place.Open(ctx)
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
	// The schema promises UTF-8 text. Sniff the same window read_media
	// uses and send non-text payloads there — a PNG is not a document.
	if top, _, _ := strings.Cut(http.DetectContentType(data), "/"); top != "text" {
		return service.ToolResult{}, fmt.Errorf("%q is not text — use read_media", input.Path)
	}
	// Text-sniffed but invalid UTF-8 (Latin-1, UTF-16 without BOM, …) would
	// reach the model as replacement characters — the provider JSON encoder
	// swaps bad bytes for U+FFFD without saying so, and an edit anchored on
	// the original bytes then fails. Refuse it and say how to convert.
	if !utf8.Valid(data) {
		return service.ToolResult{}, fmt.Errorf("%q is not valid UTF-8 — convert it first (e.g. iconv via exec)", input.Path)
	}
	return service.ToolResult{Text: string(data)}, nil
}
