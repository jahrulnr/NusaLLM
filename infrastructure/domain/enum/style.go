package enum

// Style identifies the LLM API paradigm: completion (OpenAI chat),
// responses (OpenAI Responses), messages (Anthropic), or generatecontent
// (Gemini generateContent).
type Style string

const (
	StyleCompletion      Style = "completion"
	StyleResponses       Style = "responses"
	StyleMessages        Style = "messages"
	StyleGenerateContent Style = "generatecontent"
)

func (s Style) String() string {
	return string(s)
}

func ParseStyle(v string) (Style, bool) {
	switch s := Style(v); s {
	case StyleCompletion, StyleResponses, StyleMessages, StyleGenerateContent:
		return s, true
	}
	return "", false
}
