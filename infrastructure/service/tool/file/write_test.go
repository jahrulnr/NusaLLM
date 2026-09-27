package toolfile

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/service"
)

func TestFileWriteToolWrites(t *testing.T) {
	root := t.TempDir()
	tool := mustWriteTool(t, root)
	if tool.Schema().Name() != "write_file" {
		t.Fatalf("schema name = %q", tool.Schema().Name())
	}

	out, err := runWrite(t, tool, `{"path":"a.txt","content":"hello"}`)
	if err != nil || out != "wrote 5 bytes to a.txt" {
		t.Fatalf("write = %q, %v", out, err)
	}
	data, err := os.ReadFile(filepath.Join(root, "a.txt"))
	if err != nil || string(data) != "hello" {
		t.Fatalf("file content = %q, %v", data, err)
	}
	if info, _ := os.Stat(filepath.Join(root, "a.txt")); info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %o, want 600", info.Mode().Perm())
	}

	// Overwrite truncates rather than appends.
	if _, err := runWrite(t, tool, `{"path":"a.txt","content":"hi"}`); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(data) != "hi" {
		t.Fatalf("after truncate = %q", data)
	}

	// Missing parents are created inside the root.
	if _, err := runWrite(t, tool, `{"path":"a/b/c.txt","content":"x"}`); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "a", "b", "c.txt")); err != nil || string(data) != "x" {
		t.Fatalf("nested file = %q, %v", data, err)
	}
}

func TestFileWriteToolRejects(t *testing.T) {
	root := t.TempDir()
	tool := mustWriteTool(t, root)
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	for name, input := range map[string]string{
		"escape":   `{"path":"../evil.txt","content":"x"}`,
		"dir path": `{"path":"sub","content":"x"}`,
		"empty":    `{"path":" ","content":"x"}`,
		"no path":  `{"content":"x"}`,
		"bad json": `{oops`,
	} {
		if _, err := runWrite(t, tool, input); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "..", "evil.txt")); !os.IsNotExist(err) {
		t.Fatal("escape attempt wrote outside the root")
	}
}

func mustWriteTool(t *testing.T, root string) service.Tool {
	t.Helper()
	tool, err := NewFileWriteTool(root)
	if err != nil {
		t.Fatal(err)
	}
	return tool
}

func runWrite(t *testing.T, tool service.Tool, input string) (string, error) {
	t.Helper()
	tc, err := entity.NewToolCall("call_1", "write_file", input, enum.TooltypeOrigin)
	if err != nil {
		t.Fatal(err)
	}
	res, err := tool.Execute(context.Background(), tc, nil)
	return res.Text, err
}
