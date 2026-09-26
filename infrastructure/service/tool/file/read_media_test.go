package toolfile

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jahrulnr/llm-base/infrastructure/domain/entity"
	"github.com/jahrulnr/llm-base/infrastructure/domain/enum"
	"github.com/jahrulnr/llm-base/infrastructure/domain/service"
)

// Signature + minimal IHDR stub — DetectContentType only inspects the
// prefix, so the CRC does not need to be real.
var pngFixture = []byte{
	0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, // 8-byte signature
	0x00, 0x00, 0x00, 0x0D, // IHDR length 13
	0x49, 0x48, 0x44, 0x52, // "IHDR"
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, // 1x1
	0x08, 0x06, 0x00, 0x00, 0x00, // depth 8, RGBA
	0x1F, 0x15, 0xC4, 0x89, // CRC placeholder
}

var jpegFixture = []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46, 0x49, 0x46}

func TestReadMediaToolLoads(t *testing.T) {
	root := t.TempDir()
	tool := mustMediaTool(t, root)
	if tool.Schema().Name() != "read_media" {
		t.Fatalf("schema name = %q", tool.Schema().Name())
	}
	writeFixture(t, root, "pic.png", pngFixture)
	writeFixture(t, root, "photo.jpg", jpegFixture)
	absRoot, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}

	for name, tc := range map[string]struct {
		file      string
		mediaType string
	}{
		"png":  {"pic.png", "image/png"},
		"jpeg": {"photo.jpg", "image/jpeg"},
	} {
		res, err := runReadMedia(t, tool, `{"path":"`+tc.file+`"}`)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if res.Text == "" {
			t.Errorf("%s: empty result text", name)
		}
		if len(res.Attachments) != 1 {
			t.Fatalf("%s: attachments = %d, want 1", name, len(res.Attachments))
		}
		att := res.Attachments[0]
		wantURI := "file://" + filepath.Join(absRoot, tc.file)
		if att.URI() != wantURI {
			t.Errorf("%s: uri = %q, want %q", name, att.URI(), wantURI)
		}
		if att.MediaType() != tc.mediaType {
			t.Errorf("%s: mediaType = %q, want %q", name, att.MediaType(), tc.mediaType)
		}
		if att.Name() != tc.file {
			t.Errorf("%s: name = %q, want %q", name, att.Name(), tc.file)
		}
		if att.Modality() != enum.ModalityImage {
			t.Errorf("%s: modality = %q", name, att.Modality())
		}
	}
}

func TestReadMediaToolRejects(t *testing.T) {
	root := t.TempDir()
	tool := mustMediaTool(t, root)
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, root, "note.txt", []byte("plain text, not media\n"))
	// Sparse 11 MiB with a real PNG header: sniffed as image/png, over the
	// 10 MiB image cap — no reliance on the extension fallback.
	f, err := os.Create(filepath.Join(root, "big.png"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(pngFixture); err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(11 << 20); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	for name, input := range map[string]string{
		"escape":   `{"path":"../evil.png"}`,
		"dir path": `{"path":"sub"}`,
		"missing":  `{"path":"nope.png"}`,
		"empty":    `{"path":" "}`,
		"no path":  `{}`,
		"bad json": `{oops`,
	} {
		if _, err := runReadMedia(t, tool, input); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}

	if _, err := runReadMedia(t, tool, `{"path":"note.txt"}`); err == nil || !strings.Contains(err.Error(), "read_file") {
		t.Fatalf("text file: want redirect to read_file, got %v", err)
	}
	if _, err := runReadMedia(t, tool, `{"path":"big.png"}`); err == nil || !strings.Contains(err.Error(), "10 MiB") {
		t.Fatalf("oversize: want 10 MiB cap error, got %v", err)
	}
}

func mustMediaTool(t *testing.T, root string) service.Tool {
	t.Helper()
	tool, err := NewReadMediaTool(root)
	if err != nil {
		t.Fatal(err)
	}
	return tool
}

func writeFixture(t *testing.T, root, name string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func runReadMedia(t *testing.T, tool service.Tool, input string) (service.ToolResult, error) {
	t.Helper()
	tc, err := entity.NewToolCall("call_1", "read_media", input, enum.TooltypeOrigin)
	if err != nil {
		t.Fatal(err)
	}
	return tool.Execute(context.Background(), tc, nil)
}
