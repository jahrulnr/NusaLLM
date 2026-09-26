package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/jahrulnr/llm-base/infrastructure/delivery/cli/session"
	"github.com/jahrulnr/llm-base/infrastructure/delivery/cli/term"
	"github.com/jahrulnr/llm-base/infrastructure/domain/entity"
	"github.com/jahrulnr/llm-base/infrastructure/domain/enum"
	"github.com/jahrulnr/llm-base/infrastructure/domain/event"
	"github.com/jahrulnr/llm-base/infrastructure/domain/valueobject"
)

func testModel(t *testing.T) *replModel {
	t.Helper()
	r := &REPL{Session: &session.Session{}} // render tests never reach command dispatch
	m := newModel(r, false)
	return &m
}

func TestLiveWritesDeltaVerbatim(t *testing.T) {
	m := testModel(t)
	delta := "answer with spaces\n\tand another line"

	m.applyEvent(event.NewDeltaEvent(delta))

	if got := m.live.String(); got != delta {
		t.Fatalf("live delta = %q, want %q", got, delta)
	}
}

func TestViewWrapsLiveToTerminalWidth(t *testing.T) {
	m := testModel(t)
	m.width = 20
	m.textarea.SetWidth(20)

	m.applyEvent(event.NewDeltaEvent(strings.Repeat("long ", 20) + "tail"))

	// Every logical line the renderer counts must fit a terminal row —
	// a line that wraps visually makes the next frame under-clear and
	// the status line overwrites live text mid-sentence.
	for line := range strings.Lines(m.View()) {
		if w := ansi.StringWidth(line); w > 20 {
			t.Fatalf("view line exceeds width 20 (got %d): %q", w, line)
		}
	}
}

func TestLiveEscapesTerminalControlSequences(t *testing.T) {
	m := testModel(t)

	m.applyEvent(event.NewDeltaEvent("safe\x1b[2J\rtext"))

	if got, want := m.live.String(), "safe\\u001b[2J\\u000dtext"; got != want {
		t.Fatalf("live terminal text = %q, want %q", got, want)
	}
}

func TestLiveFormatsValidToolCallJSON(t *testing.T) {
	m := testModel(t)
	input := `{"city":"Paris","count":2}`
	call := newTestToolCall(t, "call-1", "lookup", input)

	m.applyEvent(event.NewToolCallEvent(call))

	want := "\n" + term.Dim + "[tool lookup call-1]" + term.Reset + "\n{\n  \"city\": \"Paris\",\n  \"count\": 2\n}\n"
	if got := m.live.String(); got != want {
		t.Fatalf("rendered tool call = %q, want %q", got, want)
	}
	if got := call.Input(); got != input {
		t.Fatalf("tool input mutated to %q, want %q", got, input)
	}
}

func TestLivePreservesInvalidToolCallJSON(t *testing.T) {
	m := testModel(t)
	input := `{"query": broken}`
	call := newTestToolCall(t, "call-2", "lookup", input)

	m.applyEvent(event.NewToolCallEvent(call))

	want := "\n" + term.Dim + "[tool lookup call-2]" + term.Reset + "\n" + input + "\n"
	if got := m.live.String(); got != want {
		t.Fatalf("rendered tool call = %q, want raw input %q", got, want)
	}
}

func TestLivePrintsOneToolOutputHeaderPerCall(t *testing.T) {
	m := testModel(t)
	first := newTestToolCall(t, "call-1", "lookup", `{}`)
	second := newTestToolCall(t, "call-2", "read", `{}`)

	m.applyEvent(event.NewToolOutputEvent(first, "first"))
	m.applyEvent(event.NewToolOutputEvent(first, "-second\n"))
	m.applyEvent(event.NewToolOutputEvent(second, "third"))
	m.applyEvent(event.NewToolOutputEvent(second, "-fourth"))

	got := m.live.String()
	want := term.Dim + "[tool output call-1]" + term.Reset + "\nfirst-second\n" +
		term.Dim + "[tool output call-2]" + term.Reset + "\nthird-fourth"
	if got != want {
		t.Fatalf("rendered tool output = %q, want %q", got, want)
	}
}

func TestLiveBoundsToolOutputPreview(t *testing.T) {
	m := testModel(t)
	call := newTestToolCall(t, "call-1", "exec", `{}`)

	m.applyEvent(event.NewToolOutputEvent(call, strings.Repeat("x", 4096)))
	m.applyEvent(event.NewToolOutputEvent(call, "more"))

	if got := len(m.live.String()); got > 2100 {
		t.Fatalf("tool output preview must be bounded, got %d bytes", got)
	}
	if !strings.Contains(m.live.String(), "…") {
		t.Fatal("truncated preview must carry an ellipsis marker")
	}
}

func TestLivePrintsUsageCounters(t *testing.T) {
	m := testModel(t)
	usage, err := valueobject.NewUsage(11, 3, 2, 17)
	if err != nil {
		t.Fatalf("create usage: %v", err)
	}

	m.applyEvent(event.NewFinishedEvent(enum.FinishReasonStop, usage))

	want := "\n" + term.Dim + "[usage input=11 cached_read=3 cached_write=2 output=17 total=33]" + term.Reset + "\n"
	if got := m.live.String(); got != want {
		t.Fatalf("rendered usage = %q, want %q", got, want)
	}
}

func TestLiveIgnoresTurnError(t *testing.T) {
	m := testModel(t)

	m.applyEvent(event.NewErrorEvent(errors.New("already returned by application")))

	if got := m.live.String(); got != "" {
		t.Fatalf("turn error must not render, got %q", got)
	}
}

func newTestToolCall(t *testing.T, id, name, input string) entity.ToolCall {
	t.Helper()
	call, err := entity.NewToolCall(id, name, input, enum.TooltypeOrigin)
	if err != nil {
		t.Fatalf("create tool call: %v", err)
	}
	return call
}
