package textcompletion

import "encoding/json"

// Wire DTOs for the OpenAI-shaped /chat/completions surface — also spoken
// by OpenRouter, DeepSeek, Groq, and the compatible long tail. Field names
// here are wire names; domain mapping lives in encode/decode.

// RequestBody is POST /chat/completions. Params are omitempty throughout —
// unset means "provider default", never send a zero.
type RequestBody struct {
	Model            string                     `json:"model"`
	Messages         []MessageBody              `json:"messages"`
	Tools            []ToolBody                 `json:"tools,omitempty"`
	Stream           bool                       `json:"stream,omitempty"`
	Temperature      *float64                   `json:"temperature,omitempty"`
	TopP             *float64                   `json:"top_p,omitempty"`
	TopK             *int                       `json:"top_k,omitempty"` // OpenRouter extension
	MaxTokens        *int                       `json:"max_completion_tokens,omitempty"`
	PresencePenalty  *float64                   `json:"presence_penalty,omitempty"`
	FrequencyPenalty *float64                   `json:"frequency_penalty,omitempty"`
	Seed             *int64                     `json:"seed,omitempty"`
	Stop             []string                   `json:"stop,omitempty"`
	ReasoningEffort  string                     `json:"reasoning_effort,omitempty"`
	Extra            map[string]json.RawMessage `json:"-"` // flattened at marshal
}

// MarshalJSON merges Extra into the top level — provider-specific params
// ride as raw JSON under their real wire names.
func (b RequestBody) MarshalJSON() ([]byte, error) {
	type alias RequestBody // avoid recursion; Extra stays out
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

// MessageBody is one wire message. Content is any: a bare string, or a
// parts array when the message carries attachments.
type MessageBody struct {
	Role             string         `json:"role"`
	Content          any            `json:"content,omitempty"`
	ToolCalls        []ToolCallBody `json:"tool_calls,omitempty"`
	ToolCallID       string         `json:"tool_call_id,omitempty"`
	ReasoningContent string         `json:"reasoning_content,omitempty"` // DeepSeek/OpenRouter replay
}

// ToolCallBody is a wire tool_call on an assistant message. Index appears
// only in streamed deltas (fragments addressed by position); omitted on
// complete messages.
type ToolCallBody struct {
	Index    *int   `json:"index,omitempty"`
	ID       string `json:"id"`
	Type     string `json:"type"` // always "function"
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"` // JSON string, may be malformed
	} `json:"function"`
}

// ToolBody is a wire tool schema.
type ToolBody struct {
	Type     string `json:"type"` // "function"
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters,omitempty"`
	} `json:"function"`
}

// ResponseBody is the non-streamed answer — also the per-chunk shape when
// streaming (choices carry delta instead of message).
type ResponseBody struct {
	ID      string `json:"id"`
	Model   string `json:"model"` // actually-served model — aliases resolve upstream
	Choices []struct {
		Index        int         `json:"index"`
		Message      MessageBody `json:"message"`
		Delta        MessageBody `json:"delta"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Usage *UsageBody `json:"usage"`
	Error *ErrorBody `json:"error"` // mid-stream error events carry this
}

// UsageBody maps OpenAI/OpenRouter usage — prompt_tokens is the INCLUSIVE
// total (cached tokens are a subset), so decode goes through
// NewUsageFromTotal.
type UsageBody struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	PromptDetails    struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CacheWrite int `json:"cache_write_tokens"`
	// Anthropic-via-OpenRouter spells cache write differently.
	CacheCreationInput int `json:"cache_creation_input_tokens"`
}

// ErrorBody is the error envelope shared by both providers.
type ErrorBody struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    any    `json:"code"` // string or int depending on provider
}
