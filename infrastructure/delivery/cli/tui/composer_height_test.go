package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestComposerGrowsForSoftWrappedLine(t *testing.T) {
	m := testModel(t)
	m.width = 80
	m.textarea.SetWidth(composerInputWidth(80))

	long := strings.Repeat("s", 120)
	for _, r := range long {
		m.editComposer(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}

	got := ansi.Strip(m.renderComposer())
	if !strings.Contains(got, long[:40]) {
		t.Fatalf("composer missing start of long line: %q", got)
	}
	if !strings.Contains(got, long[len(long)-20:]) {
		t.Fatalf("composer missing end of soft-wrapped line (second row hidden): %q", got)
	}
	if m.textarea.Height() < 2 {
		t.Fatalf("textarea height = %d, want at least 2 for soft-wrapped input", m.textarea.Height())
	}
}
