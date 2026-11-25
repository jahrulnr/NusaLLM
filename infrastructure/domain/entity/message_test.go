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

// Two neighbouring prose parts are one stretch of text: a caller that
// dropped the part between them (an interrupted reply drops unrecorded
// tool calls) must not leave them adjacent, or a renderer projects one
// paragraph as two documents.
func TestSetPartsCanonicalizesNeighbouringProse(t *testing.T) {
	m, _ := NewMessage(enum.RoleAssistant, "Hello")
	m.SetParts([]MessagePart{
		{Kind: MessagePartReasoning, Text: "think"},
		{Kind: MessagePartReasoning, Text: "ing"},
		{Kind: MessagePartText, Text: "Hel"},
		{Kind: MessagePartToolCall, ToolCallID: "call_1"},
		{Kind: MessagePartText, Text: "lo"},
		{Kind: MessagePartText, Text: " there"},
	})
	got := m.Parts()
	if len(got) != 4 {
		t.Fatalf("parts = %+v, want 4 (reasoning, text, call, text)", got)
	}
	if got[0].Kind != MessagePartReasoning || got[0].Text != "thinking" {
		t.Fatalf("reasoning part = %+v, want the two chunks merged", got[0])
	}
	if got[1].Kind != MessagePartText || got[1].Text != "Hel" {
		t.Fatalf("text before the call = %+v", got[1])
	}
	if got[2].Kind != MessagePartToolCall || got[2].ToolCallID != "call_1" {
		t.Fatalf("tool part = %+v, want it kept as its own part", got[2])
	}
	if got[3].Kind != MessagePartText || got[3].Text != "lo there" {
		t.Fatalf("text after the call = %+v, want the two chunks merged", got[3])
	}
}

// Two calls side by side are two events even though the kind matches.
func TestSetPartsKeepsNeighbouringCallsSeparate(t *testing.T) {
	m, _ := NewMessage(enum.RoleAssistant, "")
	m.SetParts([]MessagePart{
		{Kind: MessagePartToolCall, ToolCallID: "call_1"},
		{Kind: MessagePartToolCall, ToolCallID: "call_2"},
	})
	if got := m.Parts(); len(got) != 2 || got[0].ToolCallID != "call_1" || got[1].ToolCallID != "call_2" {
		t.Fatalf("parts = %+v, want both calls distinct", got)
	}
}

// A kept interrupted reply carries answer text only: the calls it streamed
// never reached history, so a part naming one would dangle, and the prose on
// both sides of it is one stretch — a renderer must not split it.
func TestNewInterruptedReplyDropsCallsAndJoinsText(t *testing.T) {
	msg, ok, err := NewInterruptedReply([]MessagePart{
		{Kind: MessagePartReasoning, Text: "considering"},
		{Kind: MessagePartText, Text: "Hel"},
		{Kind: MessagePartToolCall, ToolCallID: "call_1"},
		{Kind: MessagePartText, Text: "lo"},
		{Kind: MessagePartText, Text: " there"},
	})
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v, want a kept reply", ok, err)
	}
	if msg.Role() != enum.RoleAssistant || msg.Content() != "Hello there" {
		t.Fatalf("reply = %q/%q, want assistant Hello there", msg.Role(), msg.Content())
	}
	if msg.FinishReason() != enum.FinishReasonInterrupted {
		t.Fatalf("finish = %q, want interrupted", msg.FinishReason())
	}
	if len(msg.ToolCalls()) != 0 {
		t.Fatalf("tool calls = %+v, want none on a kept reply", msg.ToolCalls())
	}
	got := msg.Parts()
	want := []MessagePart{
		{Kind: MessagePartReasoning, Text: "considering"},
		{Kind: MessagePartText, Text: "Hello there"},
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("parts = %+v, want %+v (call dropped, text joined)", got, want)
	}
}

// Nothing a caller can continue: a stream that only reached tool calls, or
// only reasoning, leaves no reply at all.
func TestNewInterruptedReplyKeepsNothingWithoutAnswerText(t *testing.T) {
	cases := map[string][]MessagePart{
		"tool calls only": {{Kind: MessagePartToolCall, ToolCallID: "call_1"}},
		"reasoning only":  {{Kind: MessagePartReasoning, Text: "thinking"}},
		"empty":           nil,
	}
	for label, parts := range cases {
		msg, ok, err := NewInterruptedReply(parts)
		if err != nil {
			t.Fatalf("%s: unexpected error %v", label, err)
		}
		if ok || msg.ID() != "" || len(msg.Parts()) != 0 {
			t.Fatalf("%s: reply = %+v ok=%v, want no reply", label, msg, ok)
		}
	}
}

// A cut-off step settles: an answer the provider never closed gets the
// interrupted finish, and an origin call still waiting for a result gets a
// terminal one. A call that already answered, and an injected call the model
// never issued, are left alone.
func TestMarkInterruptedSettlesTheStep(t *testing.T) {
	msg, _ := NewMessage(enum.RoleAssistant, "partial")
	answered, _ := NewToolCall("call_done", "exec", "{}", enum.TooltypeOrigin)
	answered.SetOutput("ok")
	injected, _ := NewToolCall("call_injected", "note", "{}", enum.TooltypeInjection)
	pending, _ := NewToolCall("call_pending", "exec", "{}", enum.TooltypeOrigin)
	for _, tc := range []ToolCall{answered, injected, pending} {
		if err := msg.AddToolCall(tc); err != nil {
			t.Fatal(err)
		}
	}

	if err := msg.MarkInterrupted(); err != nil {
		t.Fatal(err)
	}
	if msg.FinishReason() != enum.FinishReasonInterrupted {
		t.Fatalf("finish = %q, want interrupted", msg.FinishReason())
	}
	for _, tc := range msg.ToolCalls() {
		switch tc.ID() {
		case "call_pending":
			if !tc.HasOutput() || tc.Output() != "interrupted by user" {
				t.Fatalf("pending call output = %q (set=%v), want the terminal marker", tc.Output(), tc.HasOutput())
			}
		case "call_done":
			if tc.Output() != "ok" {
				t.Fatalf("answered call output = %q, want it untouched", tc.Output())
			}
		case "call_injected":
			if tc.HasOutput() {
				t.Fatalf("injected call got an output: %q", tc.Output())
			}
		}
	}
}

// The provider's own verdict wins: a turn that already ended (stop, length,
// tool_calls) keeps its finish when the marker is applied.
func TestMarkInterruptedKeepsProviderFinish(t *testing.T) {
	msg, _ := NewMessage(enum.RoleAssistant, "done")
	msg.SetFinishReason(enum.FinishReasonLength)
	if err := msg.MarkInterrupted(); err != nil {
		t.Fatal(err)
	}
	if msg.FinishReason() != enum.FinishReasonLength {
		t.Fatalf("finish = %q, want the provider's length", msg.FinishReason())
	}
}
