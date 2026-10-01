package command

import "testing"

func TestAttachmentPathAcceptsQuotedSpaces(t *testing.T) {
	if got, err := AttachmentPath(`"images/a photo.png"`); err != nil || got != "images/a photo.png" {
		t.Fatalf("attachment path = %q, %v", got, err)
	}
	if _, err := AttachmentPath(`"unterminated`); err == nil {
		t.Fatal("invalid quoted attachment path should fail")
	}
}
