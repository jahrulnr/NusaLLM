package repoconversation

// T-18 — a folded steer line's input id round-trips through the record. A
// record written before the field existed decodes as "no input id" and is
// never matched by text.

import (
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
)

func TestMessageRecordInputIDRoundTrip(t *testing.T) {
	msg, err := entity.NewMessage(enum.RoleUser, "steer text")
	if err != nil {
		t.Fatal(err)
	}
	msg.SetInputID("req-42")
	record := encodeMessage(msg)
	if record.InputID != "req-42" {
		t.Fatalf("encoded inputId = %q, want req-42", record.InputID)
	}
	decoded, err := decodeMessage(record, strictDecode)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.InputID() != "req-42" {
		t.Fatalf("hydrated inputId = %q, want req-42", decoded.InputID())
	}
}

func TestMessageRecordWithoutInputIDDecodes(t *testing.T) {
	msg, err := entity.NewMessage(enum.RoleUser, "ordinary input")
	if err != nil {
		t.Fatal(err)
	}
	record := encodeMessage(msg)
	if record.InputID != "" {
		t.Fatalf("ordinary line encoded an inputId: %q", record.InputID)
	}
	// An existing record predating the field has no inputId key at all.
	decoded, err := decodeMessage(record, strictDecode)
	if err != nil {
		t.Fatalf("record without inputId must stay valid: %v", err)
	}
	if decoded.InputID() != "" {
		t.Fatalf("inputId = %q, want none", decoded.InputID())
	}
}
