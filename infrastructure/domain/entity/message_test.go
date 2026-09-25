package entity

import (
	"strings"
	"testing"

	"github.com/jahrulnr/llm-base/infrastructure/domain/enum"
	"github.com/jahrulnr/llm-base/infrastructure/domain/valueobject"
)

func TestNewMessage(t *testing.T) {
	if _, err := NewMessage("", "x"); err == nil {
		t.Fatal("expected error for empty role")
	}
	m, err := NewMessage(enum.RoleUser, "hi")
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	if m.Role() != enum.RoleUser || m.Content() != "hi" {
		t.Fatalf("unexpected message: %+v", m)
	}
}

func TestMessageSetToolOutput(t *testing.T) {
	m, _ := NewMessage(enum.RoleAssistant, "")
	tc, _ := NewToolCall("call-1", "search", "{}", enum.TooltypeOrigin)
	m.AddToolCall(tc)

	if err := m.SetToolOutput("call-1", "ok"); err != nil {
		t.Fatalf("SetToolOutput: %v", err)
	}
	if got := m.ToolCalls()[0].Output(); !strings.Contains(got, "ok") {
		t.Fatalf("output not recorded: %q", got)
	}
	if err := m.SetToolOutput("missing", "x"); err == nil {
		t.Fatal("expected error for unknown call id")
	}
}

// SetToolOutput must propagate the budget error, not swallow it.
func TestMessageSetToolOutputPropagatesBudgetError(t *testing.T) {
	m, _ := NewMessage(enum.RoleAssistant, "")
	tc, _ := NewToolCall("c", "n", "{}", enum.TooltypeOrigin)
	tc.SetOutputBudget(1)
	m.AddToolCall(tc)
	if err := m.SetToolOutput("c", "too long"); err == nil {
		t.Fatal("expected budget error to propagate")
	}
}

func TestMessageAttachments(t *testing.T) {
	m, _ := NewMessage(enum.RoleUser, "look")
	a, _ := valueobject.NewAttachment("https://x/i.png", "image/png")
	m.AddAttachment(a)
	if len(m.Attachments()) != 1 || m.Attachments()[0].Modality() != enum.ModalityImage {
		t.Fatalf("unexpected attachments: %+v", m.Attachments())
	}
}
