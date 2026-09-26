package tui

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"github.com/jahrulnr/llm-base/infrastructure/delivery/cli/commands"
	"github.com/jahrulnr/llm-base/infrastructure/delivery/cli/term"
)

// paletteMaxRows caps the visible palette window; overflow scrolls.
const paletteMaxRows = 10

// slashPalette is the live command palette derived from composer state —
// codex-style: it opens while the first line is a bare /token, filters by
// prefix, wraps navigation, and Esc dismisses until the buffer changes.
// Past the command token ("/resume tw") it switches to argument
// completion through the command's completer, so ids get picked from a
// list instead of memorized.
type slashPalette struct {
	describe  func() []commands.Info
	complete  func(context.Context, string) []commands.Info
	selected  int
	token     string // buffer the selection was computed for
	dismissed string // buffer dismissed via Esc; closed until it changes
	arg       bool   // current rows complete an argument, not the command
	// Completers hit the repository/filesystem — cache their rows per
	// buffer so View repaints and spinner ticks don't rescan.
	compToken string
	compRows  []commands.Info
}

// commandToken returns the buffer when it is one bare slash token — any
// whitespace (space, tab, newline) means the command word is finished.
func commandToken(value string) (string, bool) {
	if !strings.HasPrefix(value, "/") || strings.IndexFunc(value, unicode.IsSpace) >= 0 {
		return "", false
	}
	return value, true
}

// rows returns the rows for the buffer, or nil when the palette is
// closed: command-name matches for a bare token, the command's argument
// completions once the token is finished. Selection resets whenever the
// buffer changes.
func (p *slashPalette) rows(ctx context.Context, value string) []commands.Info {
	p.arg = false
	if value == p.dismissed || !strings.HasPrefix(value, "/") {
		return nil
	}
	if value != p.token {
		p.token = value
		p.selected = 0
	}
	var matched []commands.Info
	if token, ok := commandToken(value); ok {
		for _, info := range p.describe() {
			name, _, _ := strings.Cut(info.Usage, " ")
			if strings.HasPrefix(name, token) {
				matched = append(matched, info)
			}
		}
	} else if p.complete != nil && !strings.ContainsRune(value, '\n') {
		if value == p.compToken {
			matched = p.compRows
		} else {
			matched = p.complete(ctx, value)
			p.compToken, p.compRows = value, matched
		}
		p.arg = len(matched) > 0
	}
	if len(matched) == 0 {
		return nil
	}
	if p.selected >= len(matched) {
		p.selected = 0
	}
	return matched
}

// move advances the selection with wraparound.
func (p *slashPalette) move(delta int, rows []commands.Info) {
	n := len(rows)
	if n == 0 {
		return
	}
	p.selected = (p.selected + delta + n) % n
}

// dismiss hides the palette until the buffer changes — in argument mode
// that is the whole command line, not just the token.
func (p *slashPalette) dismiss(value string) {
	p.dismissed = value
}

// completion returns the canonical command name of the selected row —
// the part before the usage args, e.g. "/resume" for "/resume <id>".
func selectedName(rows []commands.Info, selected int) string {
	name, _, _ := strings.Cut(rows[selected].Usage, " ")
	return name
}

// view renders the palette rows: usage column, dim description, selected
// row reverse-video. A "tab to select" hint rides along as a dim footer.
func (p *slashPalette) view(rows []commands.Info) string {
	width := 0
	for _, info := range rows {
		if len(info.Usage) > width {
			width = len(info.Usage)
		}
	}
	var b strings.Builder
	// The selection must stay inside the visible window — scroll the
	// window with it instead of letting the highlight walk off-screen.
	start := p.selected - paletteMaxRows + 1
	if start < 0 {
		start = 0
	}
	if start+paletteMaxRows > len(rows) {
		start = len(rows) - paletteMaxRows
	}
	if start < 0 {
		start = 0
	}
	shown := len(rows) - start
	if shown > paletteMaxRows {
		shown = paletteMaxRows
	}
	if start > 0 {
		fmt.Fprintf(&b, "  %s↑ %d more%s\n", term.Dim, start, term.Reset)
	}
	for i := start; i < start+shown; i++ {
		row := "  " + rows[i].Usage + strings.Repeat(" ", width-len(rows[i].Usage)) + "  " + term.Dim + rows[i].Desc + term.Reset
		if i == p.selected {
			row = term.Reverse + row + term.Reset
		}
		b.WriteString(row)
		b.WriteByte('\n')
	}
	if start+shown < len(rows) {
		fmt.Fprintf(&b, "  %s↓ %d more%s\n", term.Dim, len(rows)-start-shown, term.Reset)
	}
	// FillOnly rows fill the composer instead of dispatching — the footer
	// must not promise an action Enter won't take.
	verb := "select"
	if rows[p.selected].FillOnly {
		verb = "fill"
	}
	b.WriteString("  " + term.Dim + "↑↓ navigate · tab complete · enter " + verb + " · esc close" + term.Reset)
	return b.String()
}
