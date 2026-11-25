package enum

// TurnEventKind identifies which payload a TurnEvent carries.
type TurnEventKind string

const (
	// TurnEventDelta carries an incremental text chunk.
	TurnEventDelta TurnEventKind = "delta"
	// TurnEventReasoning carries an incremental provider reasoning chunk.
	TurnEventReasoning TurnEventKind = "reasoning"
	// TurnEventReasoningSummary carries an incremental provider reasoning
	// summary chunk.
	TurnEventReasoningSummary TurnEventKind = "reasoning_summary"
	// TurnEventToolCall carries a completed tool call.
	TurnEventToolCall TurnEventKind = "tool_call"
	// TurnEventToolOutput carries a live output chunk from a running tool
	// call — the call id rides in ToolCall(), the chunk in Delta().
	TurnEventToolOutput TurnEventKind = "tool_output"
	// TurnEventToolResult carries a call's terminal verdict once its result
	// is recorded — the call id, outcome and exit status ride in
	// ToolCall(). It is emitted once per call, after persistence.
	TurnEventToolResult TurnEventKind = "tool_result"
	// TurnEventSteer carries a user steering message folded into the
	// running turn — the text rides in Delta().
	TurnEventSteer TurnEventKind = "steer"
	// TurnEventFinished is the terminal event: finish reason plus usage.
	TurnEventFinished TurnEventKind = "finished"
	// TurnEventError reports a mid-stream failure.
	TurnEventError TurnEventKind = "error"
	// TurnEventStatus reports a harness-side wait while no provider output
	// moves — the TurnStatus rides in Status(), the pause in Wait().
	TurnEventStatus TurnEventKind = "status"
)

func (k TurnEventKind) String() string {
	return string(k)
}

func ParseTurnEventKind(v string) (TurnEventKind, bool) {
	switch k := TurnEventKind(v); k {
	case TurnEventDelta, TurnEventReasoning, TurnEventReasoningSummary, TurnEventToolCall, TurnEventToolOutput, TurnEventToolResult, TurnEventSteer, TurnEventFinished, TurnEventError, TurnEventStatus:
		return k, true
	}
	return "", false
}
