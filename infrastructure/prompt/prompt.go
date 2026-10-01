package prompt

import "embed"

//go:embed system/*.md user/*.md
var PromptsFS embed.FS

// SystemPrompt returns embedded system prompt text. Names are code
// literals addressing compile-time assets — a failed lookup is a
// programmer error, so it panics rather than silently disabling the
// prompt.
func SystemPrompt(name string) string {
	if len(name) < 3 || name[len(name)-3:] != ".md" {
		name += ".md"
	}
	data, err := PromptsFS.ReadFile("system/" + name)
	if err != nil {
		panic("prompt: missing embedded system prompt " + name + ": " + err.Error())
	}
	return string(data)
}

// UserPrompt returns embedded user prompt text under the same contract
// as SystemPrompt.
func UserPrompt(name string) string {
	if len(name) < 3 || name[len(name)-3:] != ".md" {
		name += ".md"
	}
	data, err := PromptsFS.ReadFile("user/" + name)
	if err != nil {
		panic("prompt: missing embedded user prompt " + name + ": " + err.Error())
	}
	return string(data)
}
