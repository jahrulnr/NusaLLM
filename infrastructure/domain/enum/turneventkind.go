package enum

// TurnEventKind identifies which payload a TurnEvent carries.
type TurnEventKind string

const (
	// TurnEventDelta carries an incremental text chunk.
	TurnEventDelta TurnEventKind = "delta"
	// TurnEventReasoning carries an incremental reasoning/thinking chunk —
	// a preview of model scratchpad text, never the answer itself.
	TurnEventReasoning TurnEventKind = "reasoning"
	// TurnEventToolCall carries a completed tool call.
	TurnEventToolCall TurnEventKind = "tool_call"
	// TurnEventToolOutput carries a live output chunk from a running tool
	// call — the call id rides in ToolCall(), the chunk in Delta().
	TurnEventToolOutput TurnEventKind = "tool_output"
	// TurnEventFinished is the terminal event: finish reason plus usage.
	TurnEventFinished TurnEventKind = "finished"
	// TurnEventError reports a mid-stream failure.
	TurnEventError TurnEventKind = "error"
)

func (k TurnEventKind) String() string {
	return string(k)
}

func ParseTurnEventKind(v string) (TurnEventKind, bool) {
	switch k := TurnEventKind(v); k {
	case TurnEventDelta, TurnEventReasoning, TurnEventToolCall, TurnEventToolOutput, TurnEventFinished, TurnEventError:
		return k, true
	}
	return "", false
}
