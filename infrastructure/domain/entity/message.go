package entity

import (
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

// Message is a single provider-agnostic conversation entry. Tool calls and
// their results are kept on the message that produced them. The id is
// permanent — edit/regenerate features address messages by it, never by
// position (compaction shifts indexes).
type Message struct {
	id              string
	role            enum.Role
	content         string
	userType        enum.LLMUserType
	finishReason    enum.FinishReason
	parts           []MessagePart
	toolCalls       []ToolCall
	attachments     []valueobject.Attachment
	optionalContent map[string]string
	inputID         string
	createdAt       time.Time
}

// MessagePart records the provider-independent order of assistant text,
// reasoning, and tool calls. Tool-call parts refer to the call stored on
// the same message.
type MessagePart struct {
	Kind       string
	Text       string
	ToolCallID string
}

const (
	MessagePartText      = "text"
	MessagePartReasoning = "reasoning"
	MessagePartToolCall  = "tool_call"
)

func NewMessage(role enum.Role, content string) (Message, error) {
	id, _ := valueobject.NewID("m", "") // minted ids are valid by construction
	msg := Message{id: id.String(), role: role, content: content, createdAt: time.Now()}
	return msg, msg.validate()
}

func (m Message) validate() error {
	if _, ok := enum.ParseRole(m.role.String()); !ok {
		return fmt.Errorf("role %q is invalid", m.role)
	}
	return nil
}

func (m Message) ID() string      { return m.id }
func (m Message) Role() enum.Role { return m.role }
func (m Message) Content() string { return m.content }

// Editable reports whether the message may be rewritten — real user
// input only. Synthetic user-role artifacts (compaction checkpoints
// carry userType=system) are not the user's words; rewriting one would
// forge user intent.
func (m Message) Editable() bool {
	return EditableMessage(m.role, m.userType)
}

// EditableMessage is the same rule on the storage tuple — wire records
// (repository rewrite) hold role/userType before hydration.
func EditableMessage(role enum.Role, userType enum.LLMUserType) bool {
	return role == enum.RoleUser && userType != enum.LLMUserTypeSystem
}

// SetContent replaces the message text — the edit primitive. Only the
// aggregate decides which messages may be rewritten (user-role only).
func (m *Message) SetContent(v string)               { m.content = v }
func (m Message) UserType() enum.LLMUserType         { return m.userType }
func (m Message) FinishReason() enum.FinishReason    { return m.finishReason }
func (m Message) Parts() []MessagePart               { return m.parts }
func (m Message) ToolCalls() []ToolCall              { return m.toolCalls }
func (m Message) OptionalContent() map[string]string { return m.optionalContent }

// clone returns a copy whose optionalContent map is detached from the
// receiver — sanitizers edit provider replay artifacts without mutating
// the stored history they were handed.
func (m Message) clone() Message {
	out := m
	if m.optionalContent != nil {
		out.optionalContent = make(map[string]string, len(m.optionalContent))
		maps.Copy(out.optionalContent, m.optionalContent)
	}
	return out
}

// WithOptionalContent returns a copy carrying one provider replay artifact.
// The receiver is unchanged.
func (m Message) WithOptionalContent(key, value string) Message {
	out := m.clone()
	out.SetOptionalContent(key, value)
	return out
}

// WithoutOptionalContent returns a copy with the named provider replay
// artifacts removed. The receiver is unchanged.
func (m Message) WithoutOptionalContent(keys ...string) Message {
	out := m.clone()
	for _, key := range keys {
		delete(out.optionalContent, key)
	}
	return out
}

// SetOptionalContent stores a provider-specific replay artifact under an
// adapter-namespaced key (e.g. "gemini.thought_signature",
// "deepseek.reasoning_content"). Values are plain strings — adapters owning a
// key parse back non-string values; payloads needing structure get a
// dedicated field instead. The domain never interprets them.
func (m *Message) SetOptionalContent(key, value string) {
	if m.optionalContent == nil {
		m.optionalContent = map[string]string{}
	}
	m.optionalContent[key] = value
}

// SetUserType records the provenance of a user-role message: real user input
// vs a system payload such as compaction output.
func (m *Message) SetUserType(t enum.LLMUserType) {
	m.userType = t
}

// SetInputID records the transport input a user line was folded from — the
// turn.steer requestId that submitted it. Only mid-turn steering carries
// one: ordinary input has no transport identity, and a line without an id
// is never matched by its text.
func (m *Message) SetInputID(id string) {
	m.inputID = id
}

// InputID is the submitting input's identity, "" when the line arrived
// without one (ordinary input, CLI steer, records written before the
// field existed).
func (m Message) InputID() string { return m.inputID }

// SetFinishReason records the normalized reason the model ended this turn.
func (m *Message) SetFinishReason(r enum.FinishReason) {
	m.finishReason = r
}

// SetParts records the ordered assistant response content in canonical
// form: neighbouring parts of the same prose kind describe one stretch of
// text, so they merge. Callers build parts by appending as chunks arrive,
// and a caller that drops a part between two prose parts (an interrupted
// reply drops tool calls that never reached history) would otherwise leave
// two adjacent text parts — indistinguishable on the wire, but rendered as
// two separate blocks downstream. Tool-call parts never merge: each names
// a different call.
func (m *Message) SetParts(parts []MessagePart) {
	if len(parts) == 0 {
		m.parts = nil
		return
	}
	out := make([]MessagePart, 0, len(parts))
	for _, p := range parts {
		if n := len(out); n > 0 && mergeablePart(out[n-1].Kind, p.Kind) {
			out[n-1].Text += p.Text
			continue
		}
		out = append(out, p)
	}
	m.parts = out
}

// NewInterruptedReply builds the assistant reply a call streamed before it
// broke: the prose the owner already saw, in arrival order, marked
// interrupted. It is the definition of "the answer we kept from a failed
// turn", so every caller records the same shape.
//
// Two rules make it a reply rather than a replay of the stream. Tool-call
// parts are dropped — their calls never reached history, and a part naming
// a call the message does not carry would dangle. Answer text is required:
// reasoning alone has nothing a retry can continue from, so ok=false means
// keep nothing (no message, no finish marker).
//
// Parts go through SetParts, so dropping a call between two text chunks
// merges them into the one stretch they always were.
func NewInterruptedReply(parts []MessagePart) (Message, bool, error) {
	var text strings.Builder
	kept := make([]MessagePart, 0, len(parts))
	for _, p := range parts {
		switch p.Kind {
		case MessagePartText:
			text.WriteString(p.Text)
		case MessagePartToolCall:
			continue
		}
		kept = append(kept, p)
	}
	if text.Len() == 0 {
		return Message{}, false, nil
	}
	msg, err := NewMessage(enum.RoleAssistant, text.String())
	if err != nil {
		return Message{}, false, err
	}
	msg.SetParts(kept)
	msg.SetFinishReason(enum.FinishReasonInterrupted)
	return msg, true, nil
}

// MarkInterrupted records that this reply was cut off before the provider
// finished the step: the finish reason becomes interrupted when the provider
// gave none, and every origin call still awaiting a result gets the terminal
// "interrupted by user" output plus the canceled verdict. An empty finish
// would read as a complete answer to a later reload, and a call without a
// result would stay pending forever; both are settled here.
//
// canceled is this path's alone: no tool can know the harness stopped the
// turn, so the one writer of that verdict is the code that cancelled it.
//
// Incomplete argument streams are not on the message — adapters omit them
// before appending, so a call that never reached history is not marked.
func (m *Message) MarkInterrupted() error {
	if m.finishReason == "" {
		m.finishReason = enum.FinishReasonInterrupted
	}
	for i := range m.toolCalls {
		tc := &m.toolCalls[i]
		if tc.Type() != enum.TooltypeOrigin || tc.HasOutput() {
			continue
		}
		if err := m.SetToolOutput(tc.ID(), "interrupted by user"); err != nil {
			return err
		}
		tc.SetOutcome(enum.ToolOutcomeCanceled, nil)
	}
	return nil
}

// SetToolOutcome records the verdict for one call, matching it by the
// provider-issued call id. A call that already carries an output keeps it —
// the verdict and the text arrive together.
func (m *Message) SetToolOutcome(id string, outcome enum.ToolOutcome, exit *valueobject.ExitStatus) error {
	for i := range m.toolCalls {
		if m.toolCalls[i].ID() == id {
			m.toolCalls[i].SetOutcome(outcome, exit)
			return nil
		}
	}
	return errors.New("tool call not found: " + id)
}

// mergeablePart reports whether two neighbouring parts are one stretch of
// the same prose. Only the kinds that carry text merge; every other kind
// (a tool call) is a distinct event even when two sit side by side.
func mergeablePart(prev, next string) bool {
	if prev != next {
		return false
	}
	return prev == MessagePartText || prev == MessagePartReasoning
}

func (m Message) CreatedAt() time.Time { return m.createdAt }

// SetCreatedAt restores the persisted timestamp. Repository hydration only.
func (m *Message) SetCreatedAt(t time.Time) { m.createdAt = t }

// SetID restores the persisted id. Repository hydration only — ids are
// generated at construction, never reassigned.
func (m *Message) SetID(id string) { m.id = id }

// AddToolCall records a call on the message. The provider-issued id is
// the only correlation key between a call and its output, so it must be
// unique within the message: a duplicate makes SetToolOutput target the
// first entry twice and leaves the second call permanently unresolved.
func (m *Message) AddToolCall(t ToolCall) error {
	for _, existing := range m.toolCalls {
		if existing.ID() == t.ID() {
			return fmt.Errorf("duplicate tool call id %q", t.ID())
		}
	}
	m.toolCalls = append(m.toolCalls, t)
	return nil
}

// AddAttachment attaches a non-text content reference (image, audio,
// video, file) to the message.
func (m *Message) AddAttachment(a valueobject.Attachment) {
	m.attachments = append(m.attachments, a)
}

func (m Message) Attachments() []valueobject.Attachment { return m.attachments }

// SetToolOutput records the result of a previously added tool call, matching
// it by the provider-issued call id.
func (m *Message) SetToolOutput(id, output string) error {
	for i := range m.toolCalls {
		if m.toolCalls[i].ID() == id {
			m.toolCalls[i].SetOutput(output)
			return nil
		}
	}
	return errors.New("tool call not found: " + id)
}

// SetToolOutputAttachments records media references produced by a tool
// call, matching it by the provider-issued call id. References carry no
// envelope — runtime and hydration use this same setter.
func (m *Message) SetToolOutputAttachments(id string, atts []valueobject.Attachment) error {
	for i := range m.toolCalls {
		if m.toolCalls[i].ID() == id {
			m.toolCalls[i].SetOutputAttachments(atts)
			return nil
		}
	}
	return errors.New("tool call not found: " + id)
}

// SetStoredToolOutput restores a persisted raw tool output and latency.
// Repository hydration and patch application only.
func (m *Message) SetStoredToolOutput(id, output, latency string) error {
	for i := range m.toolCalls {
		if m.toolCalls[i].ID() == id {
			m.toolCalls[i].SetStoredOutput(output, latency)
			return nil
		}
	}
	return errors.New("tool call not found: " + id)
}
