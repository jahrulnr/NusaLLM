package entity

import (
	"strings"
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
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

// SetToolOutput retains raw results; the budget applies to provider context.
func TestMessageSetToolOutputKeepsOverBudgetRawResult(t *testing.T) {
	m, _ := NewMessage(enum.RoleAssistant, "")
	tc, _ := NewToolCall("c", "n", "{}", enum.TooltypeOrigin)
	tc.SetOutputBudget(1)
	m.AddToolCall(tc)
	if err := m.SetToolOutput("c", "too long"); err != nil {
		t.Fatal(err)
	}
	got := m.ToolCalls()[0]
	if !got.NeedTruncate() || got.Output() != "too long" {
		t.Fatalf("raw output was rejected: %q", got.Output())
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
