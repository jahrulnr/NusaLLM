package enum

// StreamEventKind identifies which payload a StreamEvent carries.
type StreamEventKind string

const (
	// StreamEventDelta carries an incremental text chunk.
	StreamEventDelta StreamEventKind = "delta"
	// StreamEventToolCall carries a completed tool call.
	StreamEventToolCall StreamEventKind = "tool_call"
	// StreamEventFinished is the terminal event: finish reason plus usage.
	StreamEventFinished StreamEventKind = "finished"
	// StreamEventError reports a mid-stream failure.
	StreamEventError StreamEventKind = "error"
)

func (k StreamEventKind) String() string {
	return string(k)
}

func ParseStreamEventKind(v string) (StreamEventKind, bool) {
	switch k := StreamEventKind(v); k {
	case StreamEventDelta, StreamEventToolCall, StreamEventFinished, StreamEventError:
		return k, true
	}
	return "", false
}
