package workspace

// T-29 — /attach resolves its argument through the workspace port, exactly
// like every other path argument: a leading ~ is the owner's home, not a
// folder named "~" inside the workspace.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	repoworkspace "github.com/jahrulnr/nusallm/infrastructure/repository/workspace"
)

func TestResolveAttachmentExpandsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	media := filepath.Join(home, "media", "shot.png")
	if err := os.MkdirAll(filepath.Dir(media), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(media, cliPNGFixture, 0o600); err != nil {
		t.Fatal(err)
	}
	// The workspace is somewhere else entirely: ~ must not resolve inside it.
	w, err := Open(repoworkspace.NewLocal(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })

	att, err := w.ResolveAttachment(t.Context(), "~/media/shot.png")
	if err != nil {
		t.Fatalf("~/media/shot.png: %v", err)
	}
	if !strings.HasSuffix(att.URI(), "/media/shot.png") || att.Name() != "shot.png" {
		t.Fatalf("attachment = %s (%s), want the home file", att.URI(), att.Name())
	}
}

// A file: URL names the file it says — same rule, same resolver.
func TestResolveAttachmentAcceptsFileURL(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "shot.png"), cliPNGFixture, 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := Open(repoworkspace.NewLocal(), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })

	att, err := w.ResolveAttachment(t.Context(), "file://"+filepath.Join(root, "shot.png"))
	if err != nil {
		t.Fatalf("file URL: %v", err)
	}
	if att.Name() != "shot.png" {
		t.Fatalf("attachment name = %q, want shot.png", att.Name())
	}
}
