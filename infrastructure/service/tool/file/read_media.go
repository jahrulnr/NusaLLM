package toolfile

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/jahrulnr/llm-base/infrastructure/domain/entity"
	"github.com/jahrulnr/llm-base/infrastructure/domain/enum"
	"github.com/jahrulnr/llm-base/infrastructure/domain/service"
	"github.com/jahrulnr/llm-base/infrastructure/domain/valueobject"
)

// ReadMediaTool attaches a non-text file under a fixed root to the call
// result — the constructor carries the schema and validates the sandbox
// boundary; Execute resolves the call input against an os.Root so `..`
// traversal and symlink escapes fail alike. Payload bytes never enter the
// tool: the result carries a file:// reference adapters resolve at send
// time, so size caps are enforced here from the stat, not the read.
type ReadMediaTool struct {
	root     *os.Root
	rootPath string // absolute root — os.Root does not expose it
	schema   valueobject.ToolSchema
}

// NewReadMediaTool binds the tool to root: every requested path resolves
// inside it — os.Root confines traversal and symlink escapes alike. The
// absolute path is kept alongside because file:// attachment URIs need it
// and the root cannot hand it back.
func NewReadMediaTool(root string) (service.Tool, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(abs)
	if err != nil {
		return nil, err
	}
	schema, err := valueobject.NewToolSchema(
		"read_media",
		"Load an image, audio, video, or other non-text media file relative to the workspace root so it can be inspected directly. For text files use read_file.",
		json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Path relative to the workspace root"}},"required":["path"]}`),
		true,
		enum.ToolPermissionAgent,
	)
	if err != nil {
		_ = r.Close()
		return nil, err
	}
	return &ReadMediaTool{root: r, rootPath: abs, schema: schema}, nil
}

func (tool *ReadMediaTool) Schema() valueobject.ToolSchema {
	return tool.schema
}

func (tool *ReadMediaTool) Execute(ctx context.Context, call entity.ToolCall, _ service.ToolEmitter) (service.ToolResult, error) {
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
	info, err := tool.root.Stat(input.Path)
	if err != nil {
		return service.ToolResult{}, err
	}
	if info.IsDir() {
		return service.ToolResult{}, fmt.Errorf("%q is a directory", input.Path)
	}
	f, err := tool.root.Open(input.Path)
	if err != nil {
		return service.ToolResult{}, err
	}
	// Sniff the first 512 bytes — DetectContentType's window. The extension
	// is caller-supplied and says nothing about the actual payload.
	prefix, err := io.ReadAll(io.LimitReader(f, 512))
	_ = f.Close()
	if err != nil {
		return service.ToolResult{}, err
	}
	mediaType := http.DetectContentType(prefix)
	if mediaType == "application/octet-stream" {
		// Unrecognized binary: the extension is the last hint before
		// settling for a generic file.
		if byExt := mime.TypeByExtension(filepath.Ext(input.Path)); byExt != "" {
			mediaType = byExt
		}
	}
	att, err := valueobject.NewAttachment("file://"+filepath.Join(tool.rootPath, input.Path), mediaType)
	if err != nil {
		return service.ToolResult{}, err
	}
	modality := att.Modality()
	if modality == enum.ModalityText {
		// Sniffed text (a .csv IS text) belongs to read_file — this tool is
		// for payloads that cannot flow back as plain output.
		return service.ToolResult{}, fmt.Errorf("%q is text (%s) — use read_file instead", input.Path, mediaType)
	}
	// Cap by modality from the stat, before the reference is handed out —
	// adapters resolve file:// by reading the file at send time, so an
	// oversized payload would otherwise fail late and less legibly.
	var limit int64
	switch modality {
	case enum.ModalityImage:
		limit = 10 << 20
	case enum.ModalityAudio:
		limit = 50 << 20
	case enum.ModalityVideo:
		limit = 100 << 20
	default:
		limit = 20 << 20
	}
	if info.Size() > limit {
		return service.ToolResult{}, fmt.Errorf("%q exceeds the %d MiB limit for %s media", input.Path, limit>>20, modality)
	}
	att.SetName(filepath.Base(input.Path))
	return service.ToolResult{
		Text:        fmt.Sprintf("loaded %s %s (%s, %d bytes) — it is now attached for the model", modality, att.Name(), mediaType, info.Size()),
		Attachments: []valueobject.Attachment{att},
	}, nil
}
