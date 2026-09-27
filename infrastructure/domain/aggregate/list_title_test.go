package aggregate

import (
	"testing"

	"github.com/jahrulnr/llm-base/infrastructure/domain/entity"
	"github.com/jahrulnr/llm-base/infrastructure/domain/enum"
)

func TestListDisplayTitle(t *testing.T) {
	user, _ := entity.NewMessage(enum.RoleUser, "  hello   world  ")
	assistant, _ := entity.NewMessage(enum.RoleAssistant, "ignore me")

	if got := ListDisplayTitle("My chat", nil); got != "My chat" {
		t.Fatalf("stored title = %q", got)
	}
	if got := ListDisplayTitle("", []entity.Message{assistant, user}); got != "hello world" {
		t.Fatalf("fallback = %q", got)
	}
	if got := ListDisplayTitle("untitled", []entity.Message{user}); got != "hello world" {
		t.Fatalf("untitled fallback = %q", got)
	}
	if got := ListDisplayTitle("", nil); got != "untitled" {
		t.Fatalf("missing = %q", got)
	}
}

func TestListDisplayTitleMatchesPrefix(t *testing.T) {
	if !ListDisplayTitleMatchesPrefix("conv_1", "Bug", "Bug hunt", "conv") {
		t.Fatal("expected id prefix match")
	}
	if !ListDisplayTitleMatchesPrefix("conv_1", "", "Find order", "order") {
		t.Fatal("expected display title match")
	}
	if ListDisplayTitleMatchesPrefix("conv_1", "Other", "Other", "nope") {
		t.Fatal("expected no match")
	}
	if !ListDisplayTitleMatchesPrefix("x", "y", "z", "") {
		t.Fatal("empty prefix matches all")
	}
}
