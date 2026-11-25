package toolfile

// T-16 — text that crosses platforms or encodings: a CRLF file patched with
// an LF anchor, and a non-UTF-8 file that must not reach the provider JSON.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A model writes an LF anchor; the file carries CRLF. The patch applies, the
// replacement keeps the file's endings, and the rest of the file is untouched.
func TestPatchCRLFFileWithLFAnchor(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "win.txt")
	if err := os.WriteFile(path, []byte("alpha\r\nbeta\r\ngamma\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := mustPatchTool(t, root)
	if _, err := runPatch(t, tool, `{"path":"win.txt","old_string":"beta\ngamma","new_string":"BETA\nGAMMA"}`); err != nil {
		t.Fatalf("LF anchor in a CRLF file: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "alpha\r\nBETA\r\nGAMMA\r\n"; string(data) != want {
		t.Fatalf("patched file = %q, want %q", data, want)
	}
}

// A CRLF anchor against an LF file is the mirror case: the miss must stay a
// plain "not found" — the file is not rewritten to adopt CRLF.
func TestPatchLFAnchorOnlyOnCRLF(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "unix.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := mustPatchTool(t, root)
	if _, err := runPatch(t, tool, `{"path":"unix.txt","old_string":"alpha\r\nmissing","new_string":"x"}`); err == nil {
		t.Fatal("a CRLF anchor must not match an LF file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "alpha\nbeta\n" {
		t.Fatalf("a failed patch rewrote the file: %q", data)
	}
}

// Latin-1 bytes sniff as text but are not valid UTF-8; read_file refuses
// with a conversion hint instead of leaking U+FFFD into the provider JSON.
func TestFileReadRejectsInvalidUTF8(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "latin1.txt"), []byte{'c', 'a', 'f', 0xe9, '\n'}, 0o600); err != nil {
		t.Fatal(err)
	}
	tool := mustReadTool(t, root)
	_, err := runRead(t, tool, `{"path":"latin1.txt"}`)
	if err == nil {
		t.Fatal("invalid UTF-8 was returned as text")
	}
	if !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("error = %q, want a UTF-8 conversion hint", err)
	}
}
