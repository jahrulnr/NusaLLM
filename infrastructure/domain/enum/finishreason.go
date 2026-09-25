package enum

// FinishReason is the normalized reason the model ended a turn. Adapters map
// each provider's finishReason / stop_reason values onto these.
type FinishReason string

const (
	// FinishReasonStop marks a natural stop (OpenAI stop, Anthropic
	// end_turn, Gemini STOP).
	FinishReasonStop FinishReason = "stop"
	// FinishReasonToolCalls marks a turn requesting tool execution (OpenAI
	// tool_calls, Anthropic tool_use).
	FinishReasonToolCalls FinishReason = "tool_calls"
	// FinishReasonLength marks hitting the token limit; tool arguments may
	// be truncated and must not be executed.
	FinishReasonLength FinishReason = "length"
	// FinishReasonContentFilter marks a safety/filter block.
	FinishReasonContentFilter FinishReason = "content_filter"
)

func (r FinishReason) String() string {
	return string(r)
}

func ParseFinishReason(v string) (FinishReason, bool) {
	switch r := FinishReason(v); r {
	case FinishReasonStop, FinishReasonToolCalls, FinishReasonLength, FinishReasonContentFilter:
		return r, true
	}
	return "", false
}
