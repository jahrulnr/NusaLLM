package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/jahrulnr/llm-base/infrastructure/delivery/cli/term"
	"github.com/jahrulnr/llm-base/infrastructure/domain/valueobject"
)

const (
	composerPrompt     = "› "
	composerRightPad   = 1
	footerIndent       = "  "
	footerSeparator    = " · "
	footerSegmentLimit = 32
)

// composerSurface uses a fixed color: lipgloss.AdaptiveColor queries the
// terminal background over the tty on first render and can race Bubble Tea
// for the user's keystrokes.
var composerSurface = lipgloss.NewStyle().Background(lipgloss.Color("237"))

// styleComposer paints every textarea region with the surface color. The
// textarea resets styles between its prompt, text, and padding, so a
// background applied only around the whole card would leave gaps.
func styleComposer(ta *textarea.Model) {
	for _, style := range []*textarea.Style{&ta.FocusedStyle, &ta.BlurredStyle} {
		style.Base = composerSurface
		style.CursorLine = composerSurface
		style.Text = composerSurface
		style.EndOfBuffer = composerSurface
		style.Prompt = composerSurface.Bold(true)
		style.Placeholder = composerSurface.Faint(true)
	}
}

// composerInputWidth is the textarea width inside the card's right padding.
func composerInputWidth(width int) int {
	return max(1, width-composerRightPad)
}

// renderComposer frames the textarea as a full-width surface with one row
// of vertical padding, matching the Codex bottom-pane composer.
func (m replModel) renderComposer() string {
	lines := strings.Split(strings.TrimRight(m.textarea.View(), "\n"), "\n")
	width := m.width
	if width <= 0 {
		for _, line := range lines {
			width = max(width, ansi.StringWidth(line)+composerRightPad)
		}
	}
	pad := func(line string) string {
		return line + composerSurface.Render(strings.Repeat(" ", max(0, width-ansi.StringWidth(line))))
	}
	// The textarea pads its viewport with spaces emitted after a style
	// reset, so those cells would drop to the terminal background. Reopen
	// the surface color after every reset to keep the card solid.
	surface := composerSurface.Render("x")
	surface = surface[:strings.Index(surface, "x")]
	rows := make([]string, 0, len(lines)+2)
	rows = append(rows, pad(""))
	for _, line := range lines {
		if surface != "" {
			line = surface + strings.ReplaceAll(line, term.Reset, term.Reset+surface)
		}
		rows = append(rows, pad(ansi.Truncate(line, width, "")))
	}
	rows = append(rows, pad(""))
	return strings.Join(rows, "\n")
}

// renderFooter returns the context row and the instruction row shown under
// the composer. Context segments are truncated independently so a long
// path or id cannot push the route and model out of view. The idle
// instruction row yields to the palette, which carries its own key hints.
func (m replModel) renderFooter(paletteOpen bool) string {
	var badges []string
	add := func(color, text string) {
		if text != "" {
			badges = append(badges, color+term.Sanitize(truncateInline(text, footerSegmentLimit))+term.Reset)
		}
	}
	if m.repl.Workspace != nil {
		// Keep the tail of the path: the project directory is what the user
		// recognizes, not the shared prefix.
		path := []rune(displayPath(m.repl.Workspace.Path()))
		if len(path) > footerSegmentLimit {
			path = append([]rune("…"), path[len(path)-footerSegmentLimit+1:]...)
		}
		add(term.Green, string(path))
	}
	route, modelID := m.repl.Route, m.repl.ModelID
	if route == "" || modelID == "" {
		ctx := m.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		er, em := m.repl.EffectiveRouteModel(ctx)
		if route == "" {
			route = er
		}
		if modelID == "" {
			modelID = em
		}
	}
	if route != "" || modelID != "" {
		add(term.Cyan, strings.Trim(route+" / "+modelID, " /"))
	}
	if id := m.repl.ConversationID; id != "" {
		add(term.Magenta, id)
		if m.contextTotal > 0 {
			add(term.Dim, fmt.Sprintf("~%s/%s Tokens", valueobject.FormatScaledTokens(m.contextEstimate), valueobject.FormatScaledTokens(m.contextTotal)))
		}
	}
	if n := len(m.repl.Attachments); n > 0 {
		add(term.Yellow, strconv.Itoa(n)+" attached")
	}
	if n := len(m.queued); n > 0 {
		add(term.Yellow, strconv.Itoa(n)+" queued")
	}

	rows := []string{strings.Join(badges, term.Dim+footerSeparator+term.Reset)}
	if !m.turnActive && !paletteOpen {
		rows = append(rows, term.Dim+"/ for commands"+footerSeparator+"alt+enter newline"+footerSeparator+"↑ history"+term.Reset)
	}
	for i, row := range rows {
		row = footerIndent + row
		if m.width > 0 {
			row = ansi.Truncate(row, m.width, "…")
		}
		rows[i] = row
	}
	return strings.Join(rows, "\n")
}

// renderTurnStatus is the in-flight turn line — spinner, phase, interrupt
// hint — shown directly above the composer.
func (m replModel) renderTurnStatus() string {
	if !m.turnActive {
		return ""
	}
	phase := strings.TrimPrefix(m.phase, "exec ")
	if phase == "" {
		phase = "running"
	}
	row := term.Cyan + strings.TrimSpace(m.spin.View()) + " " + term.Sanitize(phase) + term.Reset +
		term.Dim + footerSeparator + "ctrl+c to interrupt" + term.Reset
	row = footerIndent + row
	if m.width > 0 {
		row = ansi.Truncate(row, m.width, "…")
	}
	return row
}

// displayPath abbreviates the home directory the way shells do.
func displayPath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if rel, err := filepath.Rel(home, path); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "~" + string(filepath.Separator) + rel
	}
	return path
}
