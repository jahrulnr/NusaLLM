package attach

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jahrulnr/llm-base/infrastructure/domain/enum"
)

var cliPNGFixture = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
	0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4, 0x89,
}

func TestResolveAttachmentPinsConfinedMedia(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootPath, "shot.png"), cliPNGFixture, 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })

	abs, err := filepath.Abs(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Resolve(root, abs, "shot.png")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(cliPNGFixture)
	if got.URI() != "file://"+filepath.Join(abs, "shot.png") || got.MediaType() != "image/png" ||
		got.Modality() != enum.ModalityImage || got.Name() != "shot.png" || got.SHA256() != hex.EncodeToString(sum[:]) {
		t.Fatalf("resolved attachment = uri:%q mime:%q modality:%q name:%q sha256:%q", got.URI(), got.MediaType(), got.Modality(), got.Name(), got.SHA256())
	}
}

func TestResolveAttachmentDoesNotTrustImageExtension(t *testing.T) {
	rootPath := t.TempDir()
	unknown := []byte{0xff, 0x00, 0x81, 0x02, 0xfd, 0x01, 0x00, 0x80}
	if err := os.WriteFile(filepath.Join(rootPath, "not-really.png"), unknown, 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	abs, err := filepath.Abs(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	att, err := Resolve(root, abs, "not-really.png")
	if err != nil {
		t.Fatal(err)
	}
	if att.MediaType() != "application/octet-stream" || att.Modality() == enum.ModalityImage {
		t.Fatalf("extension overrode detected bytes: mime=%q modality=%q", att.MediaType(), att.Modality())
	}
}

func TestResolveAttachmentRejectsUnsafeOrUnsupportedPaths(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootPath, "note.txt"), []byte("plain text"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(rootPath, "folder"), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(outside, cliPNGFixture, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(rootPath, "escape.png")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	abs, err := filepath.Abs(rootPath)
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"../outside.png", filepath.Join(abs, "note.txt"), "missing.png", "folder", "escape.png"} {
		t.Run(path, func(t *testing.T) {
			if _, err := Resolve(root, abs, path); err == nil {
				t.Fatalf("Resolve(%q) succeeded, want rejection", path)
			}
		})
	}
}

func TestResolveAttachmentEnforcesMediaSizeLimit(t *testing.T) {
	rootPath := t.TempDir()
	file, err := os.Create(filepath.Join(rootPath, "large.png"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(cliPNGFixture); err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(11 << 20); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	abs, err := filepath.Abs(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(root, abs, "large.png"); err == nil || !strings.Contains(err.Error(), "10 MiB") {
		t.Fatalf("large attachment error = %v, want 10 MiB limit", err)
	}
}
