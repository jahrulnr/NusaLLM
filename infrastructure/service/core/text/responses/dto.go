package textresponses

import "encoding/json"

// Wire DTOs for the OpenAI /v1/responses surface. The wire speaks in
// typed output/input items — message, function_call, function_call_output,
// reasoning, server-tool items — rather than role-only messages, so item
// order and raw blocks matter for replay (encrypted reasoning blobs and
// server items round-trip verbatim).

// RequestBody is POST /v1/responses. Params are omitempty throughout —
// unset means "provider default", never send a zero.
type RequestBody struct {
	Model           string                     `json:"model"`
	Input           []json.RawMessage          `json:"input"` // items pre-marshaled — template raws pass untouched
	Instructions    string                     `json:"instructions,omitempty"`
	Tools           []ToolBody                 `json:"tools,omitempty"`
	MaxOutputTokens *int                       `json:"max_output_tokens,omitempty"`
	Reasoning       *ReasoningBody             `json:"reasoning,omitempty"`
	Temperature     *float64                   `json:"temperature,omitempty"`
	TopP            *float64                   `json:"top_p,omitempty"`
	PromptCacheKey  string                     `json:"prompt_cache_key,omitempty"`
	Store           bool                       `json:"store"`
	Include         []string                   `json:"include,omitempty"`
	Stream          bool                       `json:"stream,omitempty"`
	Extra           map[string]json.RawMessage `json:"-"` // flattened at marshal
}

// MarshalJSON merges Extra into the top level — adapter-namespaced escape
// params ("responses.<param>") ride as raw JSON under their real wire names.
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

// ReasoningBody carries effort for reasoning models.
type ReasoningBody struct {
	Effort string `json:"effort,omitempty"`
}

// ItemBody is one input item on the wire — a message, a function_call the
// model made, its function_call_output, or a verbatim-replayed reasoning /
// server item. Content is any: a bare string, or a parts array of
// input_text/input_image/input_file (output_text on assistant replay).
type ItemBody struct {
	Type      string `json:"type"`
	Role      string `json:"role,omitempty"`      // message
	Content   any    `json:"content,omitempty"`   // message
	CallID    string `json:"call_id,omitempty"`   // function_call / function_call_output
	Name      string `json:"name,omitempty"`      // function_call
	Arguments string `json:"arguments,omitempty"` // function_call — JSON string
	Output    any    `json:"output,omitempty"`    // function_call_output — string or parts
}

// PartBody is one content part inside a message item or a
// function_call_output payload.
type PartBody struct {
	Type     string `json:"type"` // input_text | output_text | input_image | input_file | refusal
	Text     string `json:"text,omitempty"`
	Refusal  string `json:"refusal,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
	FileURL  string `json:"file_url,omitempty"`
	FileData string `json:"file_data,omitempty"`
	Filename string `json:"filename,omitempty"`
}

// ToolBody is a wire tool schema — flat, not function-wrapped.
type ToolBody struct {
	Type        string          `json:"type"` // "function"
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// ResponseBody is the answer — output items plus status and usage. Output
// stays raw: reasoning blobs and server items must reach the replay
// template byte-exact.
type ResponseBody struct {
	ID                string            `json:"id"`
	Status            string            `json:"status"`
	Output            []json.RawMessage `json:"output"`
	Usage             *UsageBody        `json:"usage"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Error *ErrorBody `json:"error"`
}

// ItemShape is the decoded view of one output item — fields needed by
// decode, never used for replay (raw is).
type ItemShape struct {
	Type      string          `json:"type"`
	Role      string          `json:"role"`
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Arguments string          `json:"arguments"`
	Summary   []PartBody      `json:"summary"`
	Content   []PartBody      `json:"content"`
	Encrypted json.RawMessage `json:"encrypted_content"`
}

// UsageBody maps Responses usage — input_tokens is the INCLUSIVE total
// (cached tokens are a subset), so decode goes through NewUsageFromTotal.
type UsageBody struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
	CacheWrite   int `json:"cache_write_tokens"`
	InputDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
}

// ErrorBody is the error envelope for HTTP errors and stream error events.
type ErrorBody struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    any    `json:"code"`
	Param   string `json:"param"`
}

// StreamEvent is one named SSE event's data payload — the event name is the
// type field inside it as well, so dispatch keys off the payload alone. A
// top-level "error" event IS an error body itself (code/message/param sit
// beside type), so those fields live here rather than under Error.
type StreamEvent struct {
	Type        string          `json:"type"`
	OutputIndex int             `json:"output_index"`
	Delta       string          `json:"delta"`     // text/arguments deltas
	Item        json.RawMessage `json:"item"`      // output_item.added/done
	Arguments   string          `json:"arguments"` // function_call_arguments.done
	Response    *ResponseBody   `json:"response"`  // response.completed/failed/incomplete
	Code        any             `json:"code"`      // error event
	Message     string          `json:"message"`   // error event
	Param       string          `json:"param"`     // error event
}
