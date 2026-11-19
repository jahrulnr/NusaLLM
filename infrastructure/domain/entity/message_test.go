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

// The store hydrates role through ParseRole and refuses unknown values —
// the constructor cannot accept a value the record layer would reject:
// one bogus role persisted makes the whole room unloadable.
func TestNewMessageRejectsUnknownRole(t *testing.T) {
	if _, err := NewMessage(enum.Role("bogus"), "x"); err == nil {
		t.Fatal("constructor accepted a role hydration rejects")
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

// The call id is the only correlation key between a call and its
// output; two entries sharing one id make SetToolOutput ambiguous.
func TestMessageRejectsDuplicateToolCallID(t *testing.T) {
	m, _ := NewMessage(enum.RoleAssistant, "")
	tc, _ := NewToolCall("call-1", "search", "{}", enum.TooltypeOrigin)
	if err := m.AddToolCall(tc); err != nil {
		t.Fatalf("first call: %v", err)
	}
	dup, _ := NewToolCall("call-1", "other", "{}", enum.TooltypeOrigin)
	if err := m.AddToolCall(dup); err == nil {
		t.Fatal("duplicate tool call id accepted")
	}
	if len(m.ToolCalls()) != 1 {
		t.Fatalf("calls = %d, want 1", len(m.ToolCalls()))
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
