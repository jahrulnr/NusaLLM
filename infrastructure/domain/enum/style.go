package enum

// Style identifies the LLM API paradigm: completion, responses, or messages.
type Style string

const (
	StyleCompletion Style = "completion"
	StyleResponses  Style = "responses"
	StyleMessages   Style = "messages"
)

func (s Style) String() string {
	return string(s)
}

func ParseStyle(v string) (Style, bool) {
	switch s := Style(v); s {
	case StyleCompletion, StyleResponses, StyleMessages:
		return s, true
	}
	return "", false
}
