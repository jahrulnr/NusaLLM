package entity

import (
	"errors"
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
	toolCalls       []ToolCall
	attachments     []valueobject.Attachment
	optionalContent map[string]string
	createdAt       time.Time
}

func NewMessage(role enum.Role, content string) (Message, error) {
	id, _ := valueobject.NewID("m", "") // minted ids are valid by construction
	msg := Message{id: id.String(), role: role, content: content, createdAt: time.Now()}
	return msg, msg.validate()
}

func (m Message) validate() error {
	if m.role == "" {
		return errors.New("role is required")
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
func (m Message) ToolCalls() []ToolCall              { return m.toolCalls }
func (m Message) OptionalContent() map[string]string { return m.optionalContent }

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

// SetFinishReason records the normalized reason the model ended this turn.
func (m *Message) SetFinishReason(r enum.FinishReason) {
	m.finishReason = r
}

func (m Message) CreatedAt() time.Time { return m.createdAt }

// SetCreatedAt restores the persisted timestamp. Repository hydration only.
func (m *Message) SetCreatedAt(t time.Time) { m.createdAt = t }

// SetID restores the persisted id. Repository hydration only — ids are
// generated at construction, never reassigned.
func (m *Message) SetID(id string) { m.id = id }

func (m *Message) AddToolCall(t ToolCall) {
	m.toolCalls = append(m.toolCalls, t)
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
			return m.toolCalls[i].SetOutput(output)
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

// SetStoredToolOutput restores a persisted tool output verbatim — it is
// already in the enveloped form written by SetOutput. Repository hydration
// and patch application only.
func (m *Message) SetStoredToolOutput(id, output string) error {
	for i := range m.toolCalls {
		if m.toolCalls[i].ID() == id {
			m.toolCalls[i].SetStoredOutput(output)
			return nil
		}
	}
	return errors.New("tool call not found: " + id)
}
