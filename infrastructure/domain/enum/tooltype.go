package enum

// Tooltype distinguishes a tool call originated by the model from a
// hydration entry the application injects into the conversation.
type Tooltype string

const (
	// TooltypeInjection marks a hydration tool: the application injects it
	// into the conversation to supply context, e.g. announcements,
	// subagent results, or follow-up information.
	TooltypeInjection Tooltype = "injection"
	// TooltypeOrigin marks a tool call that originated from the model and
	// is executed by the application.
	TooltypeOrigin Tooltype = "origin"
)

func (t Tooltype) String() string {
	return string(t)
}

func ParseTooltype(v string) (Tooltype, bool) {
	switch t := Tooltype(v); t {
	case TooltypeInjection, TooltypeOrigin:
		return t, true
	}
	return "", false
}
