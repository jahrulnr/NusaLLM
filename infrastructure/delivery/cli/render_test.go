package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/jahrulnr/llm-base/infrastructure/domain/entity"
	"github.com/jahrulnr/llm-base/infrastructure/domain/enum"
	"github.com/jahrulnr/llm-base/infrastructure/domain/event"
	"github.com/jahrulnr/llm-base/infrastructure/domain/valueobject"
)

func TestTurnRendererWritesDeltaVerbatim(t *testing.T) {
	var out bytes.Buffer
	r := newTurnRenderer(&out)
	delta := "answer with spaces\n\tand another line"

	r.Handle(event.NewDeltaEvent(delta))

	if got := out.String(); got != delta {
		t.Fatalf("rendered delta = %q, want %q", got, delta)
	}
	if err := r.Err(); err != nil {
		t.Fatalf("renderer error = %v, want nil", err)
	}
}

func TestTurnRendererEscapesTerminalControlSequences(t *testing.T) {
	var out bytes.Buffer
	r := newTurnRenderer(&out)

	r.Handle(event.NewDeltaEvent("safe\x1b[2J\rtext"))

	if got, want := out.String(), `safe\u001b[2J\u000dtext`; got != want {
		t.Fatalf("rendered terminal text = %q, want %q", got, want)
	}
}

func TestTurnRendererFormatsValidToolCallJSON(t *testing.T) {
	var out bytes.Buffer
	r := newTurnRenderer(&out)
	input := `{"city":"Paris","count":2}`
	call := newTestToolCall(t, "call-1", "lookup", input)

	r.Handle(event.NewToolCallEvent(call))

	want := "\n[tool lookup call-1]\n{\n  \"city\": \"Paris\",\n  \"count\": 2\n}\n"
	if got := out.String(); got != want {
		t.Fatalf("rendered tool call = %q, want %q", got, want)
	}
	if got := call.Input(); got != input {
		t.Fatalf("tool input mutated to %q, want %q", got, input)
	}
}

func TestTurnRendererPreservesInvalidToolCallJSON(t *testing.T) {
	var out bytes.Buffer
	r := newTurnRenderer(&out)
	input := `{"query": broken}`
	call := newTestToolCall(t, "call-2", "lookup", input)

	r.Handle(event.NewToolCallEvent(call))

	want := "\n[tool lookup call-2]\n" + input + "\n"
	if got := out.String(); got != want {
		t.Fatalf("rendered tool call = %q, want raw input %q", got, want)
	}
}

func TestTurnRendererPrintsOneToolOutputHeaderPerCall(t *testing.T) {
	var out bytes.Buffer
	r := newTurnRenderer(&out)
	first := newTestToolCall(t, "call-1", "lookup", `{}`)
	second := newTestToolCall(t, "call-2", "read", `{}`)

	r.Handle(event.NewToolOutputEvent(first, "first"))
	r.Handle(event.NewToolOutputEvent(first, "-second\n"))
	r.Handle(event.NewToolOutputEvent(second, "third"))
	r.Handle(event.NewToolOutputEvent(second, "-fourth"))

	want := "\n[tool output call-1]\nfirst-second\n\n[tool output call-2]\nthird-fourth"
	if got := out.String(); got != want {
		t.Fatalf("rendered tool output = %q, want %q", got, want)
	}
	if got := strings.Count(out.String(), "[tool output call-1]"); got != 1 {
		t.Fatalf("first call output header count = %d, want 1", got)
	}
	if got := strings.Count(out.String(), "[tool output call-2]"); got != 1 {
		t.Fatalf("second call output header count = %d, want 1", got)
	}
}

func TestTurnRendererPrintsUsageCounters(t *testing.T) {
	var out bytes.Buffer
	r := newTurnRenderer(&out)
	usage, err := valueobject.NewUsage(11, 3, 2, 17)
	if err != nil {
		t.Fatalf("create usage: %v", err)
	}

	r.Handle(event.NewFinishedEvent(enum.FinishReasonStop, usage))

	want := "\n[usage input=11 cached_read=3 cached_write=2 output=17 total=33]\n"
	if got := out.String(); got != want {
		t.Fatalf("rendered usage = %q, want %q", got, want)
	}
}

func TestTurnRendererDoesNotPrintTurnError(t *testing.T) {
	var out bytes.Buffer
	r := newTurnRenderer(&out)

	r.Handle(event.NewErrorEvent(errors.New("already returned by application")))

	if got := out.String(); got != "" {
		t.Fatalf("rendered turn error = %q, want no output", got)
	}
	if err := r.Err(); err != nil {
		t.Fatalf("renderer error = %v, want nil", err)
	}
}

func TestTurnRendererCapturesFirstWriteError(t *testing.T) {
	wantErr := errors.New("terminal write failed")
	out := &failingWriter{err: wantErr}
	r := newTurnRenderer(out)

	r.Handle(event.NewDeltaEvent("first"))
	r.Handle(event.NewDeltaEvent("second"))

	if err := r.Err(); !errors.Is(err, wantErr) {
		t.Fatalf("renderer error = %v, want %v", err, wantErr)
	}
	if out.writes != 1 {
		t.Fatalf("writer calls = %d, want 1 after the first error", out.writes)
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

type failingWriter struct {
	err    error
	writes int
}

func (w *failingWriter) Write([]byte) (int, error) {
	w.writes++
	return 0, w.err
}
