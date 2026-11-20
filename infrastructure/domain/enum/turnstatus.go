package enum

// TurnStatus names a harness-side wait a running turn reports while no
// provider output is moving, so the owner can tell a pause from a stall.
type TurnStatus string

const (
	// TurnStatusRetryWait marks a pause before the harness retries a
	// transient provider failure (rate limit or upstream 5xx). The pause
	// length rides in the event.
	TurnStatusRetryWait TurnStatus = "retry_wait"
	// TurnStatusCompacting marks the harness summarizing history to fit the
	// context window before the model is called.
	TurnStatusCompacting TurnStatus = "compacting"
)

func (s TurnStatus) String() string {
	return string(s)
}

func ParseTurnStatus(v string) (TurnStatus, bool) {
	switch s := TurnStatus(v); s {
	case TurnStatusRetryWait, TurnStatusCompacting:
		return s, true
	}
	return "", false
}
