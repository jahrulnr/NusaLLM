package aggregate

import (
	"errors"

	"github.com/jahrulnr/llm-base/infrastructure/domain/entity"
	"github.com/jahrulnr/llm-base/infrastructure/domain/enum"
	"github.com/jahrulnr/llm-base/infrastructure/domain/valueobject"
)

// Edit targets — the application layer maps them to status codes.
var (
	ErrMessageNotFound = errors.New("message not found")
	ErrNotUserMessage  = errors.New("only user messages are editable")
)

// Conversation is the aggregate root for a provider-agnostic LLM
// conversation. It holds dialogue state only; routing concerns such as
// provider style and model are decided per request by the service layer, so
// a single history can be translated to any wire and survive model fallback.
// Slice and map getters share internal storage by design — this is an
// internal domain, not a defensive boundary.
type Conversation struct {
	id           string
	route        string // bound gateway/account id for resume — strict DDD: aggregate roots reference by id, resolved via Gateway repository
	modelID      string // bound model id on that route — Send may still override per request
	systemPrompt string
	title        string // optional display title for conversation lists
	messages     []entity.Message
	tools        []valueobject.ToolSchema
	usage        valueobject.Usage
}

// NewConversation creates the aggregate. An empty id mints a fresh c_<hex>
// — the caller reads it back via ID().
func NewConversation(id string) (*Conversation, error) {
	vo, err := valueobject.NewID("c", id)
	if err != nil {
		return nil, err
	}
	return &Conversation{id: vo.String()}, nil
}

// Append adds a message to the conversation history.
func (c *Conversation) Append(m entity.Message) {
	c.messages = append(c.messages, m)
}

// Compact replaces messages[:dropBefore] with a summary message — the live
// history becomes [summary] + messages[dropBefore:]. The caller must pick a
// boundary that does not split a tool-call/result pair, which would leave
// orphaned results on the wire. dropBefore is clamped to the history bounds.
func (c *Conversation) Compact(summary entity.Message, dropBefore int) {
	if dropBefore < 0 {
		dropBefore = 0
	}
	if dropBefore > len(c.messages) {
		dropBefore = len(c.messages)
	}
	c.messages = append([]entity.Message{summary}, c.messages[dropBefore:]...)
}

// RewriteFrom replaces a user message's content and drops every message
// after it — the edit-and-regenerate primitive: the live history ends at
// the edited message, so the caller resends the turn from that point.
// Only messages the aggregate currently holds are candidates; ids outside
// it report ErrMessageNotFound (sealed archives are the repository's
// concern — see RewriteFrom on the Conversation port).
func (c *Conversation) RewriteFrom(msgID, content string) error {
	for i := range c.messages {
		if c.messages[i].ID() != msgID {
			continue
		}
		if c.messages[i].Role() != enum.RoleUser {
			return ErrNotUserMessage
		}
		c.messages[i].SetContent(content)
		c.messages = c.messages[:i+1]
		return nil
	}
	return ErrMessageNotFound
}

// Last returns the most recent message, or nil when the history is empty.
// The pointer aliases the live history — it detaches silently once Append
// reallocates the backing array, so callers must re-fetch after appending.
func (c *Conversation) Last() *entity.Message {
	if len(c.messages) == 0 {
		return nil
	}
	return &c.messages[len(c.messages)-1]
}

// SetTitle records the display title shown in conversation lists — set by
// the application (user rename or auto-generated from the first turn).
func (c *Conversation) SetTitle(title string) {
	c.title = title
}

// SetSystemPrompt records the conversation-level instruction. Adapters map
// it to the provider-specific channel (instructions, system param, or a
// system/developer message).
func (c *Conversation) SetSystemPrompt(prompt string) {
	c.systemPrompt = prompt
}

// SetTools records the tool schemas the agent may call in this conversation.
// Adapters translate them into the provider's tool definition array.
func (c *Conversation) SetTools(tools []valueobject.ToolSchema) {
	c.tools = tools
}

// SetRoute binds the conversation to a route (gateway/account selector) for
// resume. The Gateway itself is resolved via its own repository — aggregate
// roots reference each other by id only.
func (c *Conversation) SetRoute(route string) {
	c.route = route
}

// SetModelID binds the conversation to a model id on its route for resume.
// Send may still override per request.
func (c *Conversation) SetModelID(modelID string) {
	c.modelID = modelID
}

// AddUsage accumulates the token accounting reported for one request —
// providers report per call, the conversation tracks the running total.
func (c *Conversation) AddUsage(u valueobject.Usage) {
	c.usage = c.usage.Add(u)
}

// EstimatedTokens approximates the conversation's context size in tokens
// using a chars/4 heuristic over everything adapters replay onto the wire:
// system prompt, message content, optionalContent values (signatures,
// reasoning content, thinking payloads), and tool call input/output.
// Attachments are billed by providers via media tokenization (image tiles,
// audio/video duration), not characters — each contributes a flat
// attachmentTokenEstimate regardless of URI form.
// Per-model tokenizers differ so no exact count exists — use this for
// budgeting/compaction decisions only; Usage() holds the provider-reported
// truth for billing.
const attachmentTokenEstimate = 1000

func (c *Conversation) EstimatedTokens() int {
	chars := len(c.systemPrompt)
	flat := 0
	for _, m := range c.messages {
		chars += len(m.Content())
		flat += len(m.Attachments()) * attachmentTokenEstimate
		for _, v := range m.OptionalContent() {
			chars += len(v)
		}
		for _, tc := range m.ToolCalls() {
			chars += len(tc.Input()) + len(tc.Output())
		}
	}
	return chars/4 + flat
}

// NeedCompaction reports whether the estimated context size has reached
// budget — the caller derives it from the current model's context window
// (e.g. model.ContextWindow() * 0.9) so headroom stays reserved for the
// reply and a model switch is reflected immediately. Budget <= 0 fails
// open: compaction is a budgeting concern, never a hard gate.
func (c *Conversation) NeedCompaction(budget int) bool {
	return budget > 0 && c.EstimatedTokens() >= budget
}

func (c Conversation) ID() string                      { return c.id }
func (c Conversation) Route() string                   { return c.route }
func (c Conversation) ModelID() string                 { return c.modelID }
func (c Conversation) SystemPrompt() string            { return c.systemPrompt }
func (c Conversation) Title() string                   { return c.title }
func (c Conversation) Messages() []entity.Message      { return c.messages }
func (c Conversation) Tools() []valueobject.ToolSchema { return c.tools }
func (c Conversation) Usage() valueobject.Usage        { return c.usage }
