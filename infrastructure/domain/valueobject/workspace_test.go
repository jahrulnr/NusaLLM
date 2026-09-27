package valueobject

import (
	"errors"
	"testing"
)

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
