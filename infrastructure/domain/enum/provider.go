package enum

type Provider string

const (
	OpenAI      Provider = "api.openai.com"
	OpenRouter  Provider = "openrouter.ai"
	OpenCode    Provider = "opencode.ai"
	Anthropic   Provider = "api.anthropic.com"
	Deepseek    Provider = "api.deepseek.com"
	Gemini      Provider = "generativelanguage.googleapis.com"
	GLM         Provider = "api.z.ai"
	TokenRouter Provider = "api.tokenrouter.com"
)

func (r Provider) String() string {
	return string(r)
}

func ParseProvider(v string) (Provider, bool) {
	switch r := Provider(v); r {
	case OpenAI, OpenRouter, OpenCode, Anthropic, Deepseek, Gemini, GLM, TokenRouter:
		return r, true
	}
	return "unknown", false
}
