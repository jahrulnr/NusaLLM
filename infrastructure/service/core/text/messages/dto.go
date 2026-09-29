package textmessages

import "encoding/json"

// Wire DTOs for the Anthropic /v1/messages surface. Field names here are
// wire names; domain mapping lives in encode/decode. Content is block-
// shaped end to end — text, thinking, tool_use, tool_result — and
// assistant replay must preserve block order, so blocks round-trip as
// raw JSON where the domain has no corresponding field.

// RequestBody is POST /v1/messages. Params are omitempty throughout —
// unset means "provider default", never send a zero. max_tokens is the
// exception: the wire refuses requests without it.
type RequestBody struct {
	Model         string                     `json:"model"`
	MaxTokens     int                        `json:"max_tokens"`
	System        string                     `json:"system,omitempty"`
	Messages      []MessageBody              `json:"messages"`
	Tools         []ToolBody                 `json:"tools,omitempty"`
	OutputConfig  *OutputConfig              `json:"output_config,omitempty"`
	StopSequences []string                   `json:"stop_sequences,omitempty"`
	Temperature   *float64                   `json:"temperature,omitempty"`
	TopP          *float64                   `json:"top_p,omitempty"`
	TopK          *int                       `json:"top_k,omitempty"`
	Stream        bool                       `json:"stream,omitempty"`
	Metadata      *MetadataBody              `json:"metadata,omitempty"`
	Extra         map[string]json.RawMessage `json:"-"` // flattened at marshal
}

// MarshalJSON merges Extra into the top level — adapter-namespaced escape
// params ("messages.<param>") ride as raw JSON under their real wire names.
func (b RequestBody) MarshalJSON() ([]byte, error) {
	type alias RequestBody
	raw, err := json.Marshal(alias(b))
	if err != nil {
		return nil, err
	}
	if len(b.Extra) == 0 {
		return raw, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	for k, v := range b.Extra {
		m[k] = v
	}
	return json.Marshal(m)
}

// OutputConfig carries output shaping — effort on current models.
type OutputConfig struct {
	Effort string `json:"effort,omitempty"`
}

// MetadataBody carries opaque abuse-detection correlation — a conversation
// id, never PII.
type MetadataBody struct {
	UserID string `json:"user_id,omitempty"`
}

// MessageBody is one wire turn. Role is only ever user|assistant — the
// system prompt is a top-level field and tool results ride as tool_result
// blocks inside a user turn. Content is any: a bare string, or a block
// array when the turn carries media, thinking, or tool blocks.
type MessageBody struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

// ContentBlock is one block inside a wire message or response. The union
// is flat on the wire, so the struct is too; which fields are populated
// depends on Type.
type ContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`    // thinking block text
	Signature string          `json:"signature,omitempty"`   // thinking block signature
	Data      string          `json:"data,omitempty"`        // redacted_thinking payload
	ID        string          `json:"id,omitempty"`          // tool_use
	Name      string          `json:"name,omitempty"`        // tool_use
	Input     json.RawMessage `json:"input,omitempty"`       // tool_use — JSON object
	ToolUseID string          `json:"tool_use_id,omitempty"` // tool_result
	Content   any             `json:"content,omitempty"`     // tool_result — string or blocks
	IsError   bool            `json:"is_error,omitempty"`    // tool_result
	Source    *SourceBody     `json:"source,omitempty"`      // image / document
}

// SourceBody is the media source of an image/document block — a base64
// payload, a remote URL, or inline plain text (document blocks only).
type SourceBody struct {
	Type      string `json:"type"` // base64 | url | text
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

// ToolBody is a wire tool schema — input_schema instead of parameters.
type ToolBody struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// ResponseBody is the non-streamed answer — content blocks plus a
// stop_reason and disjoint usage counters. Content stays raw: thinking
// signatures and server blocks must reach the replay template byte-exact,
// not round-tripped through the ContentBlock union.
type ResponseBody struct {
	ID         string            `json:"id"`
	Type       string            `json:"type"`
	Role       string            `json:"role"`
	Model      string            `json:"model"`
	Content    []json.RawMessage `json:"content"`
	StopReason string            `json:"stop_reason"`
	Usage      *UsageBody        `json:"usage"`
	Error      *ErrorBody        `json:"error"` // mid-stream error events carry this
}

// UsageBody maps Anthropic usage — input_tokens is ALREADY the uncached
// count (cache read/write are disjoint fields), so decode goes through
// NewUsage, not NewUsageFromTotal.
type UsageBody struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

// ErrorBody is the error envelope shared by HTTP errors and error events.
type ErrorBody struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// StreamEvent is one named SSE event's data payload. Which fields are
// populated depends on the event name (message_start, content_block_*,
// message_delta, error).
type StreamEvent struct {
	Type         string        `json:"type"`
	Index        int           `json:"index"`
	Message      *ResponseBody `json:"message"`       // message_start
	ContentBlock *ContentBlock `json:"content_block"` // content_block_start
	Delta        *DeltaBody    `json:"delta"`         // content_block_delta, message_delta
	Usage        *UsageBody    `json:"usage"`         // message_delta — cumulative
	Error        *ErrorBody    `json:"error"`
}

// DeltaBody is the delta payload of content_block_delta and message_delta
// events — text/thinking/signature/partial_json for block deltas, the
// stop_reason carrier for message_delta.
type DeltaBody struct {
	Type        string `json:"type"`
	Text        string `json:"text"`
	Thinking    string `json:"thinking"`
	Signature   string `json:"signature"`
	PartialJSON string `json:"partial_json"`
	StopReason  string `json:"stop_reason"`
}
