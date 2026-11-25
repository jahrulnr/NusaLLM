package entity

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

// ToolCall is a record of a tool invocation. ttype marks provenance: origin
// calls come from the model, injection calls are hydration entries the
// application inserts into the conversation (announcements, subagent
// results, ...).
type ToolCall struct {
	id                string
	name              string
	input             string
	output            string
	outputSet         bool
	outputLatency     string
	outputBudget      int // Maximum output bytes sent to provider context
	outputAttachments []valueobject.Attachment
	outcome           enum.ToolOutcome
	exit              *valueobject.ExitStatus
	ttype             enum.Tooltype
	callAt            time.Time
	optionalContent   map[string]string
}

func (t ToolCall) validateInput() error {
	if t.id == "" {
		return errors.New("id is required")
	}
	if t.name == "" {
		return errors.New("name is required")
	}
	if t.input == "" {
		return errors.New("input is required")
	}
	if _, ok := enum.ParseTooltype(t.ttype.String()); !ok {
		return fmt.Errorf("tooltype %q is invalid", t.ttype)
	}
	return nil
}

func NewToolCall(id, name, input string, ttype enum.Tooltype) (ToolCall, error) {
	tc := ToolCall{
		id:           id,
		name:         name,
		input:        input,
		outputBudget: 32000,
		ttype:        ttype,
		callAt:       time.Now(),
	}

	return tc, tc.validateInput()
}

// NewUnreadableToolCall is the placeholder for a stored call whose record
// could not be decoded. It keeps whatever fields survived and carries no
// provenance (Type() is empty) and no verdict, so a reader can tell it apart
// from a real origin or injection call instead of guessing what it was.
//
// It is a read-side shape only: nothing constructs one for a live call, and
// the write path keeps validating records.
func NewUnreadableToolCall(id, name, input string) ToolCall {
	return ToolCall{id: id, name: name, input: input, outputBudget: 32000}
}

func (t ToolCall) ID() string    { return t.id }
func (t ToolCall) Name() string  { return t.name }
func (t ToolCall) Input() string { return t.input }

// Output is the exact result recorded for the call, without provider markup.
func (t ToolCall) Output() string { return t.output }

// HasOutput distinguishes a completed empty result from a pending call.
func (t ToolCall) HasOutput() bool { return t.outputSet }

// Outcome is the recorded verdict on the side effect: ok, failed, canceled.
// The zero value means no verdict was recorded — a call still pending, or a
// record written before the field existed. It is never inferred from Output.
func (t ToolCall) Outcome() enum.ToolOutcome { return t.outcome }

// Exit is how the process behind the call ended, nil when none ran or the
// record carries none.
func (t ToolCall) Exit() *valueobject.ExitStatus { return t.exit }

// OutputLatency is fixed when the result arrives; replay must not recompute it.
func (t ToolCall) OutputLatency() string { return t.outputLatency }

// NeedTruncate reports whether the raw result exceeds the context budget.
// The raw result remains available to owner-facing consumers and persistence.
func (t ToolCall) NeedTruncate() bool { return len(t.output) > max(0, t.outputBudget) }

// OutputHead and OutputTail provide byte-budgeted UTF-8-safe views.
func (t ToolCall) OutputHead() string {
	return outputHead(t.output, t.outputBudget)
}

func (t ToolCall) OutputTail() string {
	return outputTail(t.output, t.outputBudget)
}

// OutputWithMiddleTruncate keeps the beginning and end of an oversized result.
func (t ToolCall) OutputWithMiddleTruncate() string {
	if !t.NeedTruncate() {
		return t.output
	}
	return t.OutputWithMiddleTruncateWindow(t.output, t.output, len(t.output), "")
}

// OutputWithMiddleTruncateWindow applies the same context rule to a bounded
// stream window. The terminal adapter supplies its retained head/tail, total
// byte count, and optional spill reference without loading the whole stream.
func (t ToolCall) OutputWithMiddleTruncateWindow(head, tail string, total int, ref string) string {
	budget := max(0, t.outputBudget)
	if total <= budget && len(head) == total {
		return head
	}
	markerSuffix := ""
	if ref != "" {
		markerSuffix = " — " + ref
	}
	// The omitted count cannot exceed total, so this reserves enough space
	// for the actual marker even after UTF-8 boundary adjustments.
	const markerFmt = "\n\n[... %d bytes omitted%s ...]\n\n"
	room := budget - len(fmt.Sprintf(markerFmt, total, markerSuffix))
	if room <= 0 {
		return outputHead(head, budget)
	}
	head = outputHead(head, room*3/5)
	tail = outputTail(tail, room-len(head))
	return head + fmt.Sprintf(markerFmt, max(0, total-len(head)-len(tail)), markerSuffix) + tail
}

func outputHead(s string, budget int) string {
	if budget <= 0 {
		return ""
	}
	if len(s) <= budget {
		return s
	}
	return trimPartialRuneSuffix(s[:budget])
}

func outputTail(s string, budget int) string {
	if budget <= 0 {
		return ""
	}
	if len(s) <= budget {
		return s
	}
	return trimPartialRunePrefix(s[len(s)-budget:])
}

// trimPartialRuneSuffix drops only a multi-byte rune the cut split at the
// end. Interior invalid bytes stay: they are payload, and scanning the
// whole window for utf8.Valid would erase everything after the first one.
func trimPartialRuneSuffix(s string) string {
	i := len(s) - 1
	cont := 0
	for i >= 0 && cont < utf8.UTFMax-1 && s[i]&0xC0 == 0x80 {
		i--
		cont++
	}
	if i < 0 {
		return ""
	}
	need := utf8LeadSize(s[i])
	if need > 1 && len(s)-i < need {
		return s[:i]
	}
	return s
}

// trimPartialRunePrefix drops continuation bytes whose lead the cut left
// behind. A lead at the front belongs to a rune that continues into the
// kept suffix; a lone invalid byte is payload and stays.
func trimPartialRunePrefix(s string) string {
	i := 0
	for i < len(s) && i < utf8.UTFMax-1 && s[i]&0xC0 == 0x80 {
		i++
	}
	return s[i:]
}

// utf8LeadSize is the expected width of a well-formed lead byte. Invalid
// bytes (including 0xFF) are width 1 so a view keeps them instead of
// treating them as a truncated rune.
func utf8LeadSize(b byte) int {
	switch {
	case b < 0x80:
		return 1
	case b&0xE0 == 0xC0 && b >= 0xC2:
		return 2
	case b&0xF0 == 0xE0:
		return 3
	case b&0xF8 == 0xF0 && b <= 0xF4:
		return 4
	default:
		return 1
	}
}

// OutputWithUntrustedTag is the provider-facing bounded evidence view.
func (t ToolCall) OutputWithUntrustedTag() string {
	if !t.outputSet {
		return ""
	}
	output := t.OutputWithMiddleTruncate()
	// Literal delimiter text must not spoof the evidence boundary.
	output = strings.ReplaceAll(output, "<untrusted_tool", "<untrusted tool")
	output = strings.ReplaceAll(output, "</untrusted_tool", "</untrusted tool")
	return "<untrusted_tool latency=\"" + t.outputLatency + "\">\n" + output + "\n</untrusted_tool>"
}

// OutputWithUntrustedTagOrInterrupted preserves a balanced provider result
// for an origin call whose execution never produced an output record.
func (t ToolCall) OutputWithUntrustedTagOrInterrupted() string {
	if !t.outputSet {
		return "<untrusted_tool>\n(interrupted — no result was recorded)\n</untrusted_tool>"
	}
	return t.OutputWithUntrustedTag()
}

func (t ToolCall) OutputBudget() int                  { return t.outputBudget }
func (t ToolCall) Type() enum.Tooltype                { return t.ttype }
func (t ToolCall) CallAt() time.Time                  { return t.callAt }
func (t ToolCall) OptionalContent() map[string]string { return t.optionalContent }

// OutputAttachments returns media references the tool produced alongside
// its text output — e.g. a read_media call's image. References only
// (file://, https://, data:); adapters resolve them at send time.
func (t ToolCall) OutputAttachments() []valueobject.Attachment { return t.outputAttachments }

// SetOutcome records the verdict that arrived with the result, together with
// the process status when one ran. The application's result path and
// hydration are the only writers: an adapter's ToolResult.Outcome lands here
// through the application, and canceled is written by the harness alone.
func (t *ToolCall) SetOutcome(outcome enum.ToolOutcome, exit *valueobject.ExitStatus) {
	t.outcome = outcome
	t.exit = exit
}

// SetOutputAttachments records media references produced by the call.
// Like raw text output, references are stored verbatim at runtime and on
// hydration; they do not consume the provider text budget.
func (t *ToolCall) SetOutputAttachments(v []valueobject.Attachment) {
	t.outputAttachments = v
}

// SetOptionalContent stores a provider-specific replay artifact on this call
// (e.g. Gemini's per-functionCall thoughtSignature) under an
// adapter-namespaced key. Values are plain strings — adapters owning a key
// parse back non-string values; payloads needing structure get a dedicated
// field instead. The domain never interprets them.
func (t *ToolCall) SetOptionalContent(key, value string) {
	if t.optionalContent == nil {
		t.optionalContent = map[string]string{}
	}
	t.optionalContent[key] = value
}

// SetCallAt restores the persisted invocation timestamp. Repository
// hydration only — runtime calls keep the constructor's time.Now().
func (t *ToolCall) SetCallAt(v time.Time) { t.callAt = v }

// SetOutputBudget overrides the provider-context output limit. Used by
// repository hydration and by applications tuning per-call budgets.
func (t *ToolCall) SetOutputBudget(n int) { t.outputBudget = n }

// SetStoredOutput restores a raw result and its fixed latency on hydration.
// The output is complete even when its text is empty.
func (t *ToolCall) SetStoredOutput(output, latency string) {
	t.output = output
	t.outputSet = true
	t.outputLatency = latency
}

// SetOutput records the raw result produced by executing this tool call.
// The budget limits provider context, not the owner-visible stored result.
func (t *ToolCall) SetOutput(output string) {
	t.output = output
	t.outputSet = true
	t.outputLatency = ""
	if !t.callAt.IsZero() {
		t.outputLatency = time.Since(t.callAt).Round(time.Millisecond).String()
	}
}
