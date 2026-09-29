package toolfile

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	"github.com/jahrulnr/nusallm/infrastructure/domain/service"
	repoworkspace "github.com/jahrulnr/nusallm/infrastructure/repository/workspace"
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

type fakeWorkspace struct {
	drepo.Workspace
	session *fakeWorkspaceSession
}

func (w fakeWorkspace) CheckRoot(string) (string, error) { return "/remote/work", nil }
func (w fakeWorkspace) Open(context.Context, string) (drepo.WorkspaceSession, error) {
	return w.session, nil
}

type fakeWorkspaceSession struct {
	drepo.WorkspaceSession
	path string
	data []byte
}

func (s *fakeWorkspaceSession) Stat(string) (fs.FileInfo, error)   { return nil, fs.ErrNotExist }
func (s *fakeWorkspaceSession) MkdirAll(string, fs.FileMode) error { return nil }
func (s *fakeWorkspaceSession) WriteFile(path string, data []byte, _ fs.FileMode) error {
	s.path, s.data = path, append([]byte(nil), data...)
	return nil
}
func (s *fakeWorkspaceSession) Close() error { return nil }

func TestFileWriteUsesInjectedWorkspace(t *testing.T) {
	session := &fakeWorkspaceSession{}
	tool, err := NewFileWriteTool(fakeWorkspace{session: session}, "/remote/work")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runWrite(t, tool, `{"path":"nested/a.txt","content":"remote"}`); err != nil {
		t.Fatal(err)
	}
	if session.path != "nested/a.txt" || string(session.data) != "remote" {
		t.Fatalf("remote store received %q = %q", session.path, session.data)
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
	tool, err := NewFileWriteTool(repoworkspace.NewLocal(), root)
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
