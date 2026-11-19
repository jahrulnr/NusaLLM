package repoconversation

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestToolOutputBelongsToConversation(t *testing.T) {
	root := t.TempDir()
	repo, err := NewToolOutputRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Reserve(context.Background(), "missing", "call-1"); err == nil {
		t.Fatal("missing conversation accepted")
	}
	if err := os.Mkdir(filepath.Join(root, "c1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Reserve(context.Background(), "../c1", "call-1"); err == nil {
		t.Fatal("unsafe conversation id accepted")
	}
	spill, err := repo.Reserve(context.Background(), "c1", "call-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(spill.Reference()); !os.IsNotExist(err) {
		t.Fatalf("reserve created a file before output spilled: %v", err)
	}
	w, err := spill.Open()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("raw output")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := spill.Open(); !os.IsExist(err) {
		t.Fatalf("spill reopened and could overwrite existing output: %v", err)
	}
	if got, err := os.ReadFile(spill.Reference()); err != nil || string(got) != "raw output" {
		t.Fatalf("spill = %q, %v", got, err)
	}
	if err := (&ConversationRepository{path: root}).Delete(context.Background(), "c1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(spill.Reference()); !os.IsNotExist(err) {
		t.Fatalf("spill survived conversation deletion: %v", err)
	}
}

func TestToolOutputRootIsAbsoluteAndRequired(t *testing.T) {
	if _, err := NewToolOutputRepository(""); err == nil {
		t.Fatal("empty output root accepted")
	}
	repo, err := NewToolOutputRepository("relative-output")
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(repo.(*ToolOutputRepository).root) {
		t.Fatalf("root = %q, want absolute", repo.(*ToolOutputRepository).root)
	}
}

// A conversation id that resolves to a directory outside the store — ".."
// reaches the store's parent, which exists — must be refused by validation
// before any filesystem call, not by a failed stat.
func TestToolOutputReserveRejectsEscapingConversationIDs(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "store")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(base, "sibling"), 0o755); err != nil {
		t.Fatal(err)
	}
	repo, err := NewToolOutputRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"..", "../sibling", ".", "a/b", `a\b`, ""} {
		if _, err := repo.Reserve(context.Background(), id, "call-1"); err == nil {
			t.Fatalf("conversation id %q accepted", id)
		}
	}
	if _, err := os.Stat(filepath.Join(base, "tool-output")); !os.IsNotExist(err) {
		t.Fatalf("reserve wrote outside the store via ..: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "sibling", "tool-output")); !os.IsNotExist(err) {
		t.Fatalf("reserve wrote into a sibling directory: %v", err)
	}
}

// Tool output is conversation-owned. A symlink planted at the spill
// directory must not redirect a write outside the conversation; reserve
// must reject the non-real directory instead of treating EEXIST as fine.
func TestToolOutputReserveRejectsSymlinkedSpillDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	base := t.TempDir()
	root := filepath.Join(base, "store")
	room := filepath.Join(root, "c1")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(room, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(room, "tool-output")); err != nil {
		t.Fatal(err)
	}
	repo, err := NewToolOutputRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	spill, err := repo.Reserve(context.Background(), "c1", "call-1")
	if err != nil {
		return // rejecting the symlinked spill directory is the safe outcome
	}
	w, err := spill.Open()
	if err != nil {
		return
	}
	_, _ = w.Write([]byte("escaped"))
	_ = w.Close()
	if _, err := os.Stat(filepath.Join(outside, filepath.Base(spill.Reference()))); err == nil {
		t.Fatal("tool output was written through a symlinked spill directory")
	}
}

// The symlink guard covers the tool-output child — but Stat on the
// conversation directory itself follows links, so a planted
// <root>/<id> symlink reaches the same escape one component earlier:
// Mkdir lands tool-output at the link's target and the exclusive write
// happens outside the store entirely.
func TestToolOutputReserveRejectsSymlinkedConversationDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	base := t.TempDir()
	root := filepath.Join(base, "store")
	outside := filepath.Join(base, "outside")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "c1")); err != nil {
		t.Fatal(err)
	}
	repo, err := NewToolOutputRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	spill, err := repo.Reserve(context.Background(), "c1", "call-1")
	if err == nil {
		resolved, evalErr := filepath.EvalSymlinks(spill.Reference())
		if evalErr == nil && !strings.HasPrefix(resolved, root+string(os.PathSeparator)) {
			t.Fatalf("spill path escapes the store root through a symlinked conversation dir: %q", resolved)
		}
		t.Fatalf("symlinked conversation dir accepted: %q", spill.Reference())
	}
	if _, err := os.Stat(filepath.Join(outside, "tool-output")); err == nil {
		t.Fatal("reserve created tool-output outside the store root")
	}
}
