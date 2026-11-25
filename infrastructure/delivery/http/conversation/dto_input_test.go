package conversation

// T-18 — the HTTP projection carries the folded input id, so a client can
// match its own steer bubble to a history row by identity.

import (
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
)

func TestMessageDTOProjectsInputID(t *testing.T) {
	msg, err := entity.NewMessage(enum.RoleUser, "steered line")
	if err != nil {
		t.Fatal(err)
	}
	msg.SetInputID("req-7")
	if got := toMessage(msg).InputID; got != "req-7" {
		t.Fatalf("inputId = %q, want req-7", got)
	}
	ordinary, err := entity.NewMessage(enum.RoleUser, "typed line")
	if err != nil {
		t.Fatal(err)
	}
	if got := toMessage(ordinary).InputID; got != "" {
		t.Fatalf("ordinary line inputId = %q, want none", got)
	}
}
