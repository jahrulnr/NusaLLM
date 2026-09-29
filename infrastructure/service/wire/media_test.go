package wire

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
	repoworkspace "github.com/jahrulnr/nusallm/infrastructure/repository/workspace"
)

func localMediaRoot(dir string) MediaRoot {
	return MediaRoot{Dir: dir, Store: repoworkspace.NewLocal()}
}

func mustAttachment(t *testing.T, uri, mt string) valueobject.Attachment {
	t.Helper()
	a, err := valueobject.NewAttachment(uri, mt)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestResolveURI(t *testing.T) {
	raw := []byte("abc")
	if u, err := ResolveURI(mustAttachment(t, "https://x/img.png", "image/png"), MediaRoot{}); err != nil || u != "https://x/img.png" {
		t.Fatal("https passthrough broken")
	}
	dataURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(raw)
	if u, err := ResolveURI(mustAttachment(t, dataURI, "image/png"), MediaRoot{}); err != nil || u != dataURI {
		t.Fatal("data: passthrough broken")
	}

	root := t.TempDir()
	rootPath, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "x.bin")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	u, err := ResolveURI(mustAttachment(t, "file://"+path, "application/octet-stream"), localMediaRoot(rootPath))
	if err != nil {
		t.Fatal(err)
	}
	if u != "data:application/octet-stream;base64,"+base64.StdEncoding.EncodeToString(raw) {
		t.Fatalf("file:// resolve = %q", u)
	}
	if _, err := ResolveURI(mustAttachment(t, "file://"+filepath.Join(root, "missing.bin"), "application/octet-stream"), localMediaRoot(rootPath)); err == nil {
		t.Fatal("missing file should error")
	}
}

// A pinned attachment must resolve to the exact bytes the producer
// hashed — a swapped file fails closed rather than silently sending
// different content.
func TestResolveURIPin(t *testing.T) {
	raw := []byte("abc")
	sum := sha256.Sum256(raw)
	pin := hex.EncodeToString(sum[:])

	root := t.TempDir()
	rootPath, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "x.bin")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	a := mustAttachment(t, "file://"+path, "application/octet-stream")
	a.SetSHA256(pin)
	if _, err := ResolveURI(a, localMediaRoot(rootPath)); err != nil {
		t.Fatalf("matching pin should resolve: %v", err)
	}

	if err := os.WriteFile(path, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveURI(a, localMediaRoot(rootPath)); err == nil {
		t.Fatal("swapped content must fail the pin")
	}

	// A pin on a remote ref cannot be verified without fetching — fail
	// closed rather than silently trusting it.
	a = mustAttachment(t, "https://x/img.png", "image/png")
	a.SetSHA256(pin)
	if _, err := ResolveURI(a, MediaRoot{}); err == nil {
		t.Fatal("pinned remote ref must be rejected")
	}

	// data: URIs carry their bytes inline — pins verify those too.
	a = mustAttachment(t, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(raw), "image/png")
	a.SetSHA256(pin)
	if _, err := ResolveURI(a, MediaRoot{}); err != nil {
		t.Fatalf("pinned data URI should resolve: %v", err)
	}
	other := sha256.Sum256([]byte("other"))
	a.SetSHA256(hex.EncodeToString(other[:]))
	if _, err := ResolveURI(a, MediaRoot{}); err == nil {
		t.Fatal("pinned data URI with wrong digest must fail")
	}
}

func TestResolveURIConfinesAndBoundsLocalFiles(t *testing.T) {
	root := t.TempDir()
	rootPath, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(rootPath, "inside.bin")
	outside := filepath.Join(t.TempDir(), "outside.bin")
	content := []byte("small binary payload")
	if err := os.WriteFile(inside, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, content, 0o600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(rootPath, "escape.bin")
	if err := os.Symlink(outside, symlink); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	att := mustAttachment(t, "file://"+inside, "application/octet-stream")
	if _, err := ResolveURI(att, localMediaRoot(rootPath)); err != nil {
		t.Fatalf("workspace file should resolve: %v", err)
	}
	att = mustAttachment(t, "file://"+outside, "application/octet-stream")
	if _, err := ResolveURI(att, localMediaRoot(rootPath)); err == nil {
		t.Fatal("file outside workspace must not resolve")
	}
	att = mustAttachment(t, "file://"+symlink, "application/octet-stream")
	if _, err := ResolveURI(att, localMediaRoot(rootPath)); err == nil {
		t.Fatal("symlink escape must not resolve")
	}

	large := filepath.Join(rootPath, "large.png")
	f, err := os.Create(large)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(11 << 20); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	att = mustAttachment(t, "file://"+large, "image/png")
	if _, err := ResolveURI(att, localMediaRoot(rootPath)); err == nil {
		t.Fatal("file over the media cap must not resolve")
	}
}

func TestResolveBase64(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString([]byte("sound"))
	data, format, err := ResolveBase64(mustAttachment(t, "data:audio/mpeg;base64,"+b64, "audio/mpeg"), MediaRoot{})
	if err != nil || data != b64 || format != "mp3" {
		t.Fatalf("data uri: %q %q %v", data, format, err)
	}
	if _, _, err := ResolveBase64(mustAttachment(t, "https://x/a.mp3", "audio/mpeg"), MediaRoot{}); err == nil {
		t.Fatal("https audio should be rejected — nothing to embed")
	}
}

func TestFormatName(t *testing.T) {
	for in, want := range map[string]string{
		"audio/mpeg": "mp3", "audio/x-wav": "wav", "audio/mp4": "mp4",
	} {
		if got := FormatName(in); got != want {
			t.Fatalf("%s → %s, want %s", in, got, want)
		}
	}
}
