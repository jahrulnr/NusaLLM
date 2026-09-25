package entity

import (
	"errors"
	"strings"
	"time"

	"github.com/jahrulnr/llm-base/infrastructure/domain/enum"
)

// ToolCall is a record of a tool invocation. ttype marks provenance: origin
// calls come from the model, injection calls are hydration entries the
// application inserts into the conversation (announcements, subagent
// results, ...).
type ToolCall struct {
	id              string
	name            string
	input           string
	output          string
	outputBudget    int // Maximum length of output
	ttype           enum.Tooltype
	callAt          time.Time
	optionalContent map[string]string
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

func (t ToolCall) ID() string                         { return t.id }
func (t ToolCall) Name() string                       { return t.name }
func (t ToolCall) Input() string                      { return t.input }
func (t ToolCall) Output() string                     { return t.output }
func (t ToolCall) OutputBudget() int                  { return t.outputBudget }
func (t ToolCall) Type() enum.Tooltype                { return t.ttype }
func (t ToolCall) CallAt() time.Time                  { return t.callAt }
func (t ToolCall) OptionalContent() map[string]string { return t.optionalContent }

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

// SetOutputBudget overrides the maximum accepted output length. Used by
// repository hydration and by applications tuning per-call budgets.
func (t *ToolCall) SetOutputBudget(n int) { t.outputBudget = n }

// SetStoredOutput restores a persisted output verbatim — it is already in
// the enveloped form written by SetOutput. Repository hydration only;
// runtime results always go through SetOutput.
func (t *ToolCall) SetStoredOutput(output string) { t.output = output }

// SetOutput records the result produced by executing this tool call. The
// output is wrapped as delimited evidence carrying the call latency, and
// literal delimiter strings inside the output are neutralized.
func (t *ToolCall) SetOutput(output string) error {
	if len(output) > t.outputBudget {
		return errors.New("output is too long")
	}

	latency := ""
	if !t.callAt.IsZero() {
		latency = time.Since(t.callAt).Round(time.Millisecond).String()
	}

	// Neutralize any delimiter prefix — including attribute-bearing variants
	// like `<untrusted_tool foo="x">` — so embedded content cannot spoof an
	// envelope boundary.
	output = strings.ReplaceAll(output, "<untrusted_tool", "<untrusted tool")
	output = strings.ReplaceAll(output, "</untrusted_tool", "</untrusted tool")

	t.output = `<untrusted_tool latency="` + latency + `">\n` + output + "\n</untrusted_tool>"

	return nil
}
