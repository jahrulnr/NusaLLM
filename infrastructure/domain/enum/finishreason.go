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
	// FinishReasonInterrupted marks an answer cut short before the
	// provider finished the step — a caller cancel, a dead process, or a
	// failed call that had already streamed text — never a provider
	// verdict. A settled assistant tail must carry one of the reasons
	// above; this marker exists so a partial reply cannot pass for a
	// finished one. Its one meaning is "incomplete step", regardless of
	// who ended it.
	FinishReasonInterrupted FinishReason = "interrupted"
)

func (r FinishReason) String() string {
	return string(r)
}

func ParseFinishReason(v string) (FinishReason, bool) {
	switch r := FinishReason(v); r {
	case FinishReasonStop, FinishReasonToolCalls, FinishReasonLength, FinishReasonContentFilter, FinishReasonInterrupted:
		return r, true
	}
	return "", false
}
