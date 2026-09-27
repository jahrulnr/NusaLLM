package tui

import (
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/styles"
	"github.com/jahrulnr/llm-base/infrastructure/delivery/cli/term"
)

const (
	defaultMarkdownWidth = 80
	minMarkdownWidth     = 20
)

// renderMarkdown turns assistant Markdown into terminal-native output. The
// live viewport re-renders the current assistant buffer as deltas arrive, so
// incomplete emphasis or code fences are handled by the parser on each frame.
func renderMarkdown(text string, width int) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	if width <= 0 {
		width = defaultMarkdownWidth
	}
	if width < minMarkdownWidth {
		width = minMarkdownWidth
	}

	style := styles.DarkStyleConfig
	style.Document.Color = nil
	margin := uint(0)
	style.Document.Margin = &margin
	renderer, err := glamour.NewTermRenderer(
		glamour.WithStyles(style),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return term.Sanitize(text)
	}
	rendered, err := renderer.Render(text)
	if err != nil {
		return term.Sanitize(text)
	}
	return strings.TrimRight(rendered, "\n")
}
