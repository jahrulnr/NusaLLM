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
	"github.com/jahrulnr/nusallm/infrastructure/service/workspace"
)

// ReadMediaTool attaches a non-text file to the call result — under the
// turn's workspace, or an absolute host path the repository resolves for a
// read. The workspace repository owns what the path argument means, so this
// adapter never expands, joins, or stats it. The constructor carries the
// schema and validates the default sandbox boundary. Payload bytes never
// enter the tool: the result carries a file:// reference adapters resolve at
// send time, so size caps are enforced here from the stat, not the read. The
// reference outlives the check, so the content itself is pinned: the sha256
// recorded here is hashed through the same handle this tool read, and
// send-time resolution verifies it. A swapped or escaped path degrades to a
// placeholder instead of sending unchecked bytes.
type ReadMediaTool struct {
	dir    string
	store  drepo.Workspace
	schema valueobject.ToolSchema
}

// NewReadMediaTool binds the tool's default root: a relative path resolves
// inside it, an absolute path anchors where it lives. A turn-scoped
// workspace dir overrides it per call; the repository creates attachment
// URIs for provider-time resolution.
func NewReadMediaTool(store drepo.Workspace, root string) (service.Tool, error) {
	abs, err := checkDir(store, root)
	if err != nil {
		return nil, err
	}
	schema, err := valueobject.NewToolSchema(
		"read_media",
		"Load an image, audio, video, or other non-text media file so it can be inspected directly. For text files use read_file.",
		json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Path relative to the workspace root, or an absolute path anywhere on this machine"}},"required":["path"]}`),
		true,
		enum.ToolPermissionAgent,
	)
	if err != nil {
		return nil, err
	}
	return &ReadMediaTool{dir: abs, store: store, schema: schema}, nil
}

func (tool *ReadMediaTool) Schema() valueobject.ToolSchema {
	return tool.schema
}

func (tool *ReadMediaTool) Execute(ctx context.Context, call entity.ToolCall, _ service.ToolEmitter) (result service.ToolResult, retErr error) {
	if err := ctx.Err(); err != nil {
		return service.ToolResult{}, err
	}
	var input struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(call.Input()), &input); err != nil {
		return service.ToolResult{}, fmt.Errorf("invalid read_media input: %w", err)
	}
	if strings.TrimSpace(input.Path) == "" {
		return service.ToolResult{}, fmt.Errorf("path is required")
	}
	// The repository decides where the argument lands: inside the turn's
	// workspace (confined) or on a host file by absolute path. The pin rule
	// itself — sniff, refuse text, size cap, hash — belongs to the workspace
	// adapter, so the tool and /attach cannot drift apart.
	place, err := tool.store.LocateRead(ctx, callDir(ctx, tool.dir), input.Path)
	if err != nil {
		return service.ToolResult{}, err
	}
	defer func() {
		if closeErr := place.Close(); retErr == nil {
			retErr = closeErr
		}
	}()
	att, size, err := workspace.PinAttachment(ctx, place, input.Path)
	if err != nil {
		return service.ToolResult{}, err
	}
	return service.ToolResult{
		Text:        fmt.Sprintf("loaded %s %s (%s, %d bytes) — it is now attached for the model", att.Modality(), att.Name(), att.MediaType(), size),
		Attachments: []valueobject.Attachment{att},
	}, nil
}
