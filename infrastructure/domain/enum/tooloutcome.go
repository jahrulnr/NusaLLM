package enum

// ToolOutcome is the verdict on one tool call's side effect: did it do what
// the model asked? It is data, not a reading of the output text — an adapter
// that ran the process decides, and the harness decides only the one value no
// tool can know (canceled).
//
// The zero value is "no verdict recorded" and is read as ok for a ToolResult
// (a tool that returns normally succeeded); a stored ToolCall keeps the zero
// value to mean "the record predates the field".
type ToolOutcome string

const (
	// ToolOutcomeOK marks a call whose side effect succeeded.
	ToolOutcomeOK ToolOutcome = "ok"
	// ToolOutcomeFailed marks a call that ran and failed — a nonzero exit,
	// a signal death, or a Go error the application recorded. The output
	// text still carries the evidence; this is the verdict.
	ToolOutcomeFailed ToolOutcome = "failed"
	// ToolOutcomeCanceled marks a call the harness stopped before it
	// finished. Only the caller that cancelled the turn can say this: a
	// tool never returns it.
	ToolOutcomeCanceled ToolOutcome = "canceled"
)

func (o ToolOutcome) String() string { return string(o) }

// ParseToolOutcome accepts the closed set plus the empty string, which means
// "no verdict recorded".
func ParseToolOutcome(v string) (ToolOutcome, bool) {
	switch o := ToolOutcome(v); o {
	case "", ToolOutcomeOK, ToolOutcomeFailed, ToolOutcomeCanceled:
		return o, true
	}
	return "", false
}
