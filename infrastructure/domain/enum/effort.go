package enum

// Effort selects the reasoning effort for a request. default and the zero
// value mean "send nothing" — the gateway/provider default applies. max
// requires validating model support before use (e.g. via the OpenRouter
// model parameters endpoint).
type Effort string

const (
	EffortDefault Effort = "default"
	EffortNone    Effort = "none"
	EffortLow     Effort = "low"
	EffortMedium  Effort = "medium"
	EffortHigh    Effort = "high"
	EffortXHigh   Effort = "xhigh"
	EffortMax     Effort = "max"
)

func (e Effort) String() string {
	return string(e)
}

// IsUnset reports whether the effort should be omitted from the wire request.
func (e Effort) IsUnset() bool {
	return e == "" || e == EffortDefault
}

func ParseEffort(v string) (Effort, bool) {
	switch e := Effort(v); e {
	case EffortDefault, EffortNone, EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax:
		return e, true
	}
	return "", false
}
