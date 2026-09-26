package wire

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/jahrulnr/llm-base/infrastructure/domain/valueobject"
)

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
	if u, err := ResolveURI(mustAttachment(t, "https://x/img.png", "image/png")); err != nil || u != "https://x/img.png" {
		t.Fatal("https passthrough broken")
	}
	dataURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(raw)
	if u, err := ResolveURI(mustAttachment(t, dataURI, "image/png")); err != nil || u != dataURI {
		t.Fatal("data: passthrough broken")
	}

	path := filepath.Join(t.TempDir(), "x.bin")
	os.WriteFile(path, raw, 0o644)
	u, err := ResolveURI(mustAttachment(t, "file://"+path, "application/octet-stream"))
	if err != nil {
		t.Fatal(err)
	}
	if u != "data:application/octet-stream;base64,"+base64.StdEncoding.EncodeToString(raw) {
		t.Fatalf("file:// resolve = %q", u)
	}
	if _, err := ResolveURI(mustAttachment(t, "file:///nope/missing.bin", "application/octet-stream")); err == nil {
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

	path := filepath.Join(t.TempDir(), "x.bin")
	os.WriteFile(path, raw, 0o644)

	a := mustAttachment(t, "file://"+path, "application/octet-stream")
	a.SetSHA256(pin)
	if _, err := ResolveURI(a); err != nil {
		t.Fatalf("matching pin should resolve: %v", err)
	}

	os.WriteFile(path, []byte("tampered"), 0o644)
	if _, err := ResolveURI(a); err == nil {
		t.Fatal("swapped content must fail the pin")
	}

	// A pin on a remote ref cannot be verified without fetching — fail
	// closed rather than silently trusting it.
	a = mustAttachment(t, "https://x/img.png", "image/png")
	a.SetSHA256(pin)
	if _, err := ResolveURI(a); err == nil {
		t.Fatal("pinned remote ref must be rejected")
	}

	// data: URIs carry their bytes inline — pins verify those too.
	a = mustAttachment(t, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(raw), "image/png")
	a.SetSHA256(pin)
	if _, err := ResolveURI(a); err != nil {
		t.Fatalf("pinned data URI should resolve: %v", err)
	}
	other := sha256.Sum256([]byte("other"))
	a.SetSHA256(hex.EncodeToString(other[:]))
	if _, err := ResolveURI(a); err == nil {
		t.Fatal("pinned data URI with wrong digest must fail")
	}
}

func TestResolveBase64(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString([]byte("sound"))
	data, format, err := ResolveBase64(mustAttachment(t, "data:audio/mpeg;base64,"+b64, "audio/mpeg"))
	if err != nil || data != b64 || format != "mp3" {
		t.Fatalf("data uri: %q %q %v", data, format, err)
	}
	if _, _, err := ResolveBase64(mustAttachment(t, "https://x/a.mp3", "audio/mpeg")); err == nil {
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
