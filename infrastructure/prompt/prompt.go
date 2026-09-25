package prompt

import "embed"

//go:embed system/*.md user/*.md
var PromptsFS embed.FS

func SystemPrompt(name string) string {
	if len(name) < 3 || name[len(name)-3:] != ".md" {
		name += ".md"
	}
	data, err := PromptsFS.ReadFile("system/" + name)
	if err != nil {
		return ""
	}
	return string(data)
}

func UserPrompt(name string) string {
	if len(name) < 3 || name[len(name)-3:] != ".md" {
		name += ".md"
	}
	data, err := PromptsFS.ReadFile("user/" + name)
	if err != nil {
		return ""
	}
	return string(data)
}
