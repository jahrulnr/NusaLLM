package valueobject

import (
	"errors"
	"testing"
)

func TestNewWorkspaceCanonicalizesValidPaths(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"x", "x"},
		{"images/a photo.png", "images/a photo.png"},
		{"a/b/c.png", "a/b/c.png"},
		{"./x", "x"},
		{"a//b", "a/b"},
		{"a/./b", "a/b"},
		{"a/b/", "a/b"},
		{"a/b/.", "a/b"},
		{".", "."},
		{".../x", ".../x"},
		{"a..b/x", "a..b/x"},
		{"dir:name/f", "dir:name/f"},
	} {
		w, err := NewWorkspace(tc.in)
		if err != nil {
			t.Fatalf("NewWorkspace(%q) error = %v, want canonical %q", tc.in, err, tc.want)
		}
		if got := w.Path(); got != tc.want {
			t.Fatalf("NewWorkspace(%q).Path() = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNewWorkspaceRejectsTraversal(t *testing.T) {
	for _, p := range []string{
		"/abs",
		"//share/x",
		"..",
		"../x",
		"a/../b",
		"a/b/..",
		"../../etc/passwd",
		"C:",
		"C:x",
		"C:/x",
		"d:/dir/f",
	} {
		if _, err := NewWorkspace(p); !errors.Is(err, ErrPathTraversal) {
			t.Fatalf("NewWorkspace(%q) error = %v, want %v", p, err, ErrPathTraversal)
		}
	}
}

func TestNewWorkspaceRejectsBackslash(t *testing.T) {
	// Windows-shaped input is non-canonical: the grammar is slash-separated
	// whatever the host OS, so backslash must not act as a separator, drive
	// or traversal smuggle.
	for _, p := range []string{
		`a\b`,
		`..\x`,
		`C:\x`,
		`\\share`,
		`\\share\x`,
		`a\b\c`,
	} {
		if _, err := NewWorkspace(p); err == nil {
			t.Fatalf("NewWorkspace(%q) succeeded, want rejection", p)
		}
	}
}

func TestNewWorkspaceRejectsEmpty(t *testing.T) {
	if _, err := NewWorkspace(""); err == nil {
		t.Fatal("NewWorkspace(\"\") succeeded, want rejection")
	}
}

func TestParseWorkspacePathUnquotesAndValidatesPath(t *testing.T) {
	workspace, err := ParseWorkspacePath(`"images/a photo.png"`)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := workspace.Path(), "images/a photo.png"; got != want {
		t.Fatalf("workspace path = %q, want %q", got, want)
	}
}

func TestParseWorkspacePathRejectsTraversalAfterUnquoting(t *testing.T) {
	_, err := ParseWorkspacePath(`"../outside.png"`)
	if !errors.Is(err, ErrPathTraversal) {
		t.Fatalf("parse workspace path error = %v, want %v", err, ErrPathTraversal)
	}
}

func TestParseWorkspacePathRejectsBackslashAfterUnquoting(t *testing.T) {
	// strconv.Unquote resolves the \\ escape to a single backslash, which the
	// workspace grammar must still reject.
	if _, err := ParseWorkspacePath(`"a\\b"`); err == nil {
		t.Fatal(`ParseWorkspacePath("a\\b") succeeded, want rejection`)
	}
}

func TestParseWorkspacePathRejectsEmptyQuoted(t *testing.T) {
	if _, err := ParseWorkspacePath(`""`); err == nil {
		t.Fatal(`ParseWorkspacePath("") succeeded, want rejection`)
	}
}
