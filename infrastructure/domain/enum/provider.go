package enum

import "strings"

// Provider enumerates known provider hosts as the anchor for per-gateway
// wire characteristics: adapters branch on provider identity for quirks
// like OpenCode's x-opencode-session header or OpenRouter's attribution
// headers and prompt_cache_key. The constants are consumed through
// MatchesEndpoint; ParseProvider is for adapters that need the branch
// explicitly. Keep this enum even while usage is thin — it is the
// registry new provider quirks are expected to key off.
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

// MatchesEndpoint reports whether a gateway endpoint names this provider.
// DNS hosts are case-insensitive, so the match must be too — an endpoint
// saved as HTTPS://OPENROUTER.AI/API is still OpenRouter.
func (r Provider) MatchesEndpoint(endpoint string) bool {
	return strings.Contains(strings.ToLower(endpoint), strings.ToLower(string(r)))
}

func ParseProvider(v string) (Provider, bool) {
	switch r := Provider(v); r {
	case OpenAI, OpenRouter, OpenCode, Anthropic, Deepseek, Gemini, GLM, TokenRouter:
		return r, true
	}
	return "unknown", false
}
