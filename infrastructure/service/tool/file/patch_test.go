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

func TestFilePatchToolPatches(t *testing.T) {
	root := t.TempDir()
	tool := mustPatchTool(t, root)
	if tool.Schema().Name() != "apply_patch" {
		t.Fatalf("schema name = %q", tool.Schema().Name())
	}
	seed := filepath.Join(root, "a.txt")
	if err := os.WriteFile(seed, []byte("hello world"), 0o750); err != nil {
		t.Fatal(err)
	}

	out, err := runPatch(t, tool, `{"path":"a.txt","old_string":"world","new_string":"there"}`)
	if err != nil || out != "patched a.txt (1 occurrence)" {
		t.Fatalf("patch = %q, %v", out, err)
	}
	if data, _ := os.ReadFile(seed); string(data) != "hello there" {
		t.Fatalf("content = %q", data)
	}
	// Mode survives the rewrite — WriteFile only applies perm on create.
	if info, _ := os.Stat(seed); info.Mode().Perm() != 0o750 {
		t.Fatalf("mode = %o, want 750", info.Mode().Perm())
	}

	// Empty new_string deletes the anchor.
	if _, err := runPatch(t, tool, `{"path":"a.txt","old_string":"hello ","new_string":""}`); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(seed); string(data) != "there" {
		t.Fatalf("after delete = %q", data)
	}
}

func TestFilePatchToolAmbiguous(t *testing.T) {
	root := t.TempDir()
	tool := mustPatchTool(t, root)
	seed := filepath.Join(root, "dup.txt")
	if err := os.WriteFile(seed, []byte("x-x-x"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Multiple matches without replace_all → error reporting the count.
	_, err := runPatch(t, tool, `{"path":"dup.txt","old_string":"x","new_string":"y"}`)
	if err == nil || !strings.Contains(err.Error(), "3 places") {
		t.Fatalf("ambiguous err = %v", err)
	}
	if data, _ := os.ReadFile(seed); string(data) != "x-x-x" {
		t.Fatalf("ambiguous patch must not write: %q", data)
	}

	out, err := runPatch(t, tool, `{"path":"dup.txt","old_string":"x","new_string":"y","replace_all":true}`)
	if err != nil || !strings.Contains(out, "3 occurrences") {
		t.Fatalf("replace_all = %q, %v", out, err)
	}
	if data, _ := os.ReadFile(seed); string(data) != "y-y-y" {
		t.Fatalf("after replace_all = %q", data)
	}
}

func TestFilePatchToolRejects(t *testing.T) {
	root := t.TempDir()
	tool := mustPatchTool(t, root)
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	for name, input := range map[string]string{
		"not found":    `{"path":"a.txt","old_string":"zzz","new_string":"y"}`,
		"empty old":    `{"path":"a.txt","old_string":"","new_string":"y"}`,
		"escape":       `{"path":"../evil.txt","old_string":"x","new_string":"y"}`,
		"dir path":     `{"path":"sub","old_string":"x","new_string":"y"}`,
		"no old":       `{"path":"a.txt","new_string":"y"}`,
		"bad json":     `{oops`,
		"missing file": `{"path":"none.txt","old_string":"x","new_string":"y"}`,
	} {
		if _, err := runPatch(t, tool, input); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func mustPatchTool(t *testing.T, root string) service.Tool {
	t.Helper()
	tool, err := NewFilePatchTool(repoworkspace.NewLocal(), root)
	if err != nil {
		t.Fatal(err)
	}
	return tool
}

func runPatch(t *testing.T, tool service.Tool, input string) (string, error) {
	t.Helper()
	tc, err := entity.NewToolCall("call_1", "apply_patch", input, enum.TooltypeOrigin)
	if err != nil {
		t.Fatal(err)
	}
	res, err := tool.Execute(context.Background(), tc, nil)
	return res.Text, err
}
