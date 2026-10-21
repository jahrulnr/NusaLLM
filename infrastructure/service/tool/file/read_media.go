package toolfile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	"github.com/jahrulnr/nusallm/infrastructure/domain/service"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
	"github.com/jahrulnr/nusallm/infrastructure/service/wire"
)

// ReadMediaTool attaches a non-text file to the call result — under the
// turn's workspace, or an absolute host path via Workspace.OpenHost.
// The constructor carries the schema and validates the
// default sandbox boundary; Execute resolves relative paths against a
// repository workspace session so `..` traversal and symlink escapes fail
// alike. Payload bytes never enter the
// tool: the result carries a file:// reference adapters resolve at send
// time, so size caps are enforced here from the stat, not the read. The
// reference outlives the check, so the content itself is pinned: the
// sha256 recorded here is hashed through this same confined handle, and
// send-time resolution verifies it. The completion adapter reopens files
// under the same workspace root; swapped or escaped paths degrade to a
// placeholder instead of sending unchecked bytes.
type ReadMediaTool struct {
	dir    string
	store  drepo.Workspace
	schema valueobject.ToolSchema
}

// NewReadMediaTool binds the tool's default root: every requested path
// resolves inside it. A turn-scoped workspace dir overrides it per call;
// the repository creates attachment URIs for provider-time resolution.
func NewReadMediaTool(store drepo.Workspace, root string) (service.Tool, error) {
	abs, err := checkDir(store, root)
	if err != nil {
		return nil, err
	}
	schema, err := valueobject.NewToolSchema(
		"read_media",
		"Load an image, audio, video, or other non-text media file so it can be inspected directly. For text files use read_file.",
		json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Path relative to the workspace root, or an absolute host path"}},"required":["path"]}`),
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
	// Absolute host paths bypass the workspace os.Root — an agent working
	// outside its workspace (a screenshot in /tmp, an asset elsewhere on
	// disk) names the file directly. The sha256 pin still binds what
	// send-time serves, so the check-time file is what the model gets.
	// Both opens produce (file, info, uri); the tail sniffs/caps/pins
	// identically.
	var f io.ReadCloser
	var info fs.FileInfo
	var uri string
	if filepath.IsAbs(input.Path) {
		var err error
		f, info, err = tool.store.OpenHost(ctx, input.Path)
		if err != nil {
			return service.ToolResult{}, fmt.Errorf("%q is not a readable regular file: %w", input.Path, err)
		}
		uri = tool.store.HostURI(input.Path)
	} else {
		root, err := callRoot(ctx, tool.store, tool.dir)
		if err != nil {
			return service.ToolResult{}, err
		}
		defer func() {
			if closeErr := root.Close(); retErr == nil {
				retErr = closeErr
			}
		}()
		f, info, err = root.OpenRegular(input.Path)
		if err != nil {
			return service.ToolResult{}, fmt.Errorf("%q is not a readable regular file: %w", input.Path, err)
		}
		uri = root.AttachmentURI(input.Path)
	}
	defer func() {
		if closeErr := f.Close(); retErr == nil {
			retErr = closeErr
		}
	}()
	// Sniff the first 512 bytes — DetectContentType's window. The extension
	// is caller-supplied and says nothing about the actual payload.
	prefix := make([]byte, 512)
	n, err := io.ReadFull(f, prefix)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return service.ToolResult{}, err
	}
	prefix = prefix[:n]
	mediaType := http.DetectContentType(prefix)
	att, err := valueobject.NewAttachment(uri, mediaType)
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
	limit := wire.MaxAttachmentBytes(modality)
	if info.Size() > limit {
		return service.ToolResult{}, fmt.Errorf("%q exceeds the %d MiB limit for %s media", input.Path, limit>>20, modality)
	}
	// Pin the content the reference will resolve to: hash through this
	// confined handle — never the raw path — so what send-time verifies is
	// provably the file os.Root allowed us to see. The read continues from
	// the sniff offset, keeping this to a single pass.
	h := sha256.New()
	_, _ = h.Write(prefix)
	rest, err := io.Copy(h, io.LimitReader(f, limit+1-int64(n)))
	if err != nil {
		return service.ToolResult{}, err
	}
	if int64(n)+rest > limit {
		return service.ToolResult{}, fmt.Errorf("%q exceeds the %d MiB limit for %s media", input.Path, limit>>20, modality)
	}
	att.SetSHA256(hex.EncodeToString(h.Sum(nil)))
	att.SetName(filepath.Base(input.Path))
	return service.ToolResult{
		Text:        fmt.Sprintf("loaded %s %s (%s, %d bytes) — it is now attached for the model", modality, att.Name(), mediaType, info.Size()),
		Attachments: []valueobject.Attachment{att},
	}, nil
}
