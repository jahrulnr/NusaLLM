package toolfile

// T-01 — the four path-bearing tools obey the repository's path rule: a
// file: URL, a leading ~ and an absolute path all resolve, a write outside
// the workspace is refused, and nothing creates a literal "./~" folder.

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/service"
)

// A home path, a file: URL and an absolute path reach the file they name
// through every tool that reads, and a write to them never lands in the
// workspace as a literal folder.
func TestFileToolsResolveExpandedPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "probe.txt"), []byte("home-file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "inside.txt"), []byte("inside-file"), 0o600); err != nil {
		t.Fatal(err)
	}
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "outside.txt")
	if err := os.WriteFile(outside, []byte("outside-file"), 0o600); err != nil {
		t.Fatal(err)
	}

	read := mustReadTool(t, workspace)
	patch := mustPatchTool(t, workspace)

	for name, input := range map[string]string{
		"home":             `{"path":"~/probe.txt"}`,
		"file url":         `{"path":"file://` + filepath.Join(workspace, "inside.txt") + `"}`,
		"absolute inside":  `{"path":"` + filepath.Join(workspace, "inside.txt") + `"}`,
		"absolute outside": `{"path":"` + outside + `"}`,
	} {
		got, err := runRead(t, read, input)
		if err != nil {
			t.Fatalf("%s: read %q: %v", name, input, err)
		}
		if !strings.Contains(got, "file") {
			t.Fatalf("%s: content = %q", name, got)
		}
	}
	// apply_patch resolves the same argument.
	if _, err := runPatch(t, patch, `{"path":"file://`+filepath.Join(workspace, "inside.txt")+`","old_string":"inside-file","new_string":"patched"}`); err != nil {
		t.Fatalf("patch through a file: URL: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(workspace, "inside.txt")); err != nil || string(data) != "patched" {
		t.Fatalf("patched file = %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "~")); err == nil {
		t.Fatal(`a read created a literal "./~" folder`)
	}
	if data, err := os.ReadFile(filepath.Join(home, "probe.txt")); err != nil || string(data) != "home-file" {
		t.Fatalf("home file changed: %q, %v", data, err)
	}
}

// Writes never leave the workspace: an absolute out-of-root path, a file: URL
// pointing there, and a ~/… path are refused, and the workspace gains no
// literal folders from the attempt.
func TestFileWriteRefusesOutsideWorkspace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "out.txt")
	write := mustWriteTool(t, workspace)
	patch := mustPatchTool(t, workspace)

	for name, input := range map[string]string{
		"absolute": `{"path":"` + outside + `","content":"x"}`,
		"file url": `{"path":"file://` + outside + `","content":"x"}`,
		"home":     `{"path":"~/probe.txt","content":"x"}`,
	} {
		_, err := runWrite(t, write, input)
		if err == nil {
			t.Fatalf("%s: write accepted %q", name, input)
		}
		if !strings.Contains(err.Error(), workspace) {
			t.Fatalf("%s: error %q must name the workspace root", name, err)
		}
	}
	if _, err := runPatch(t, patch, `{"path":"~/probe.txt","old_string":"a","new_string":"b"}`); err == nil {
		t.Fatal("apply_patch accepted a home write")
	}
	if _, err := os.Stat(outside); err == nil {
		t.Fatal("a refused write created the outside file")
	}
	if _, err := os.Stat(filepath.Join(home, "probe.txt")); err == nil {
		t.Fatal("a refused write created the home file")
	}
	for _, literal := range []string{"~", "file:"} {
		if _, err := os.Stat(filepath.Join(workspace, literal)); err == nil {
			t.Fatalf("a refused write created a literal %q folder", literal)
		}
	}
}

// read_media shares the rule: a home path and a file: URL attach the file
// they name, and the reference points at the resolved path.
func TestReadMediaResolvesExpandedPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workspace := t.TempDir()
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)
	if err := os.WriteFile(filepath.Join(home, "pic.png"), png, 0o600); err != nil {
		t.Fatal(err)
	}
	tool := mustMediaTool(t, workspace)

	for name, input := range map[string]string{
		"home":     `{"path":"~/pic.png"}`,
		"file url": `{"path":"file://` + filepath.Join(home, "pic.png") + `"}`,
	} {
		res, err := runReadMedia(t, tool, input)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(res.Attachments) != 1 {
			t.Fatalf("%s: attachments = %d", name, len(res.Attachments))
		}
		if want := "file://" + filepath.Join(home, "pic.png"); res.Attachments[0].URI() != want {
			t.Fatalf("%s: uri = %q, want %q", name, res.Attachments[0].URI(), want)
		}
	}
	if _, err := os.Stat(filepath.Join(workspace, "~")); err == nil {
		t.Fatal(`read_media created a literal "./~" folder`)
	}
}

// A Windows-shaped %VAR% stays a literal name on this host — the rule is
// Windows-only, so the tools keep treating it as a workspace path here.
func TestFileToolsPercentOnNonWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the %VAR% rule applies on Windows")
	}
	workspace := t.TempDir()
	write := mustWriteTool(t, workspace)
	if _, err := runWrite(t, write, `{"path":"%APPDATA%/c.txt","content":"x"}`); err != nil {
		t.Fatalf("write %%APPDATA%%/c.txt: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "%APPDATA%", "c.txt")); err != nil {
		t.Fatalf("a literal %%APPDATA%% folder is the Linux behavior: %v", err)
	}
}

// A tool with a turn-scoped workspace dir resolves there, not in its
// configured fallback — the ctx value is which folder the rule works in.
func TestFileToolsUseTurnWorkspace(t *testing.T) {
	fallback := t.TempDir()
	turn := t.TempDir()
	write := mustWriteTool(t, fallback)
	ctx := service.WithWorkspaceDir(t.Context(), turn)
	tc, err := entity.NewToolCall("call_1", "write_file", `{"path":"a.txt","content":"turn"}`, enum.TooltypeOrigin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := write.Execute(ctx, tc, nil); err != nil {
		t.Fatalf("write in the turn workspace: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(turn, "a.txt")); err != nil || string(data) != "turn" {
		t.Fatalf("turn workspace file = %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(fallback, "a.txt")); err == nil {
		t.Fatal("the configured fallback received the write")
	}
}
