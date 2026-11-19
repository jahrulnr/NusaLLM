package toolfile

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/service"
	repoworkspace "github.com/jahrulnr/nusallm/infrastructure/repository/workspace"
)

func TestFileReadToolReads(t *testing.T) {
	root := t.TempDir()
	tool := mustReadTool(t, root)
	if tool.Schema().Name() != "read_file" {
		t.Fatalf("schema name = %q", tool.Schema().Name())
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runRead(t, tool, `{"path":"a.txt"}`)
	if err != nil || out != "hello" {
		t.Fatalf("read = %q, %v", out, err)
	}
}

func TestFileReadToolRejects(t *testing.T) {
	root := t.TempDir()
	tool := mustReadTool(t, root)
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, input := range map[string]string{
		"escape":       `{"path":"../evil.txt"}`,
		"dir path":     `{"path":"sub"}`,
		"missing file": `{"path":"none.txt"}`,
		"bad json":     `{oops`,
	} {
		if _, err := runRead(t, tool, input); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

// The schema promises UTF-8 text, but the tool streams whatever bytes
// the confined open yields — a small binary file is returned verbatim
// into model context instead of being rejected with a redirect to
// read_media, the same check read_media applies in reverse.
func TestFileReadRejectsBinaryContent(t *testing.T) {
	root := t.TempDir()
	tool := mustReadTool(t, root)
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)
	if err := os.WriteFile(filepath.Join(root, "pic.png"), png, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := runRead(t, tool, `{"path":"pic.png"}`)
	if err == nil {
		t.Fatal("binary content returned as text — must redirect to read_media")
	}
	if !strings.Contains(err.Error(), "read_media") {
		t.Fatalf("binary read error must point at read_media: %v", err)
	}
}

func mustReadTool(t *testing.T, root string) service.Tool {
	t.Helper()
	tool, err := NewFileReadTool(repoworkspace.NewLocal(), root)
	if err != nil {
		t.Fatal(err)
	}
	return tool
}

func runRead(t *testing.T, tool service.Tool, input string) (string, error) {
	t.Helper()
	tc, err := entity.NewToolCall("call_1", "read_file", input, enum.TooltypeOrigin)
	if err != nil {
		t.Fatal(err)
	}
	res, err := tool.Execute(context.Background(), tc, nil)
	return res.Text, err
}
