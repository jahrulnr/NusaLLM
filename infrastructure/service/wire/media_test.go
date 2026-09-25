package wire

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveURI(t *testing.T) {
	raw := []byte("abc")
	if u, err := ResolveURI("https://x/img.png", "image/png"); err != nil || u != "https://x/img.png" {
		t.Fatal("https passthrough broken")
	}
	dataURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(raw)
	if u, err := ResolveURI(dataURI, "image/png"); err != nil || u != dataURI {
		t.Fatal("data: passthrough broken")
	}

	path := filepath.Join(t.TempDir(), "x.bin")
	os.WriteFile(path, raw, 0o644)
	u, err := ResolveURI("file://"+path, "application/octet-stream")
	if err != nil {
		t.Fatal(err)
	}
	if u != "data:application/octet-stream;base64,"+base64.StdEncoding.EncodeToString(raw) {
		t.Fatalf("file:// resolve = %q", u)
	}
	if _, err := ResolveURI("file:///nope/missing.bin", "application/octet-stream"); err == nil {
		t.Fatal("missing file should error")
	}
}

func TestResolveBase64(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString([]byte("sound"))
	data, format, err := ResolveBase64("data:audio/mpeg;base64,"+b64, "audio/mpeg")
	if err != nil || data != b64 || format != "mp3" {
		t.Fatalf("data uri: %q %q %v", data, format, err)
	}
	if _, _, err := ResolveBase64("https://x/a.mp3", "audio/mpeg"); err == nil {
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
