package tui

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/jahrulnr/nusallm/infrastructure/delivery/cli/session"
	"github.com/jahrulnr/nusallm/infrastructure/delivery/cli/term"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/event"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
	repohistory "github.com/jahrulnr/nusallm/infrastructure/repository/history"
	repoworkspace "github.com/jahrulnr/nusallm/infrastructure/repository/workspace"
	"github.com/jahrulnr/nusallm/infrastructure/service/workspace"
	"github.com/muesli/termenv"
)

func testModel(t *testing.T) *replModel {
	t.Helper()
	r := &REPL{Session: &session.Session{}, historyStore: repohistory.NewLocal()} // render tests never reach command dispatch
	m := newModel(r, false)
	return &m
}

func TestLiveWritesDeltaVerbatim(t *testing.T) {
	m := testModel(t)
	delta := "answer with spaces\n\tand another line"

	m.applyEvent(event.NewDeltaEvent(delta))

	if got := m.live.String(); got != delta {
		t.Fatalf("live delta = %q, want %q", got, delta)
	}
}

func TestLiveShowsReasoningInArrivalOrder(t *testing.T) {
	m := testModel(t)
	m.turnActive = true

	m.applyEvent(event.NewDeltaEvent("before"))
	m.applyEvent(event.NewReasoningEvent("raw reasoning"))
	m.applyEvent(event.NewReasoningSummaryEvent(" summary"))
	if m.phase != "thinking" {
		t.Fatalf("phase = %q, want thinking", m.phase)
	}
	m.applyEvent(event.NewDeltaEvent("after"))

	got := m.renderLive()
	before, reasoning, after := strings.Index(got, "before"), strings.Index(got, "raw reasoning summary"), strings.Index(got, "after")
	if before < 0 || reasoning < 0 || after < 0 || !(before < reasoning && reasoning < after) {
		t.Fatalf("reasoning missing or out of order: %q", got)
	}
}

func TestViewPlacesFooterBadgesUnderComposer(t *testing.T) {
	m := testModel(t)
	m.width = 80
	m.textarea.SetWidth(composerInputWidth(80))
	m.repl.ConversationID = "conv-123"
	m.repl.Route = "openrouter"
	m.repl.ModelID = "gpt-test"
	m.contextEstimate = 200_000
	m.contextTotal = 1_000_000
	m.textarea.SetValue("draft message")

	lines := strings.Split(ansi.Strip(m.View()), "\n")
	composer, footer := -1, -1
	for i, line := range lines {
		if strings.Contains(line, composerPrompt+"draft message") {
			composer = i
		}
		if strings.Contains(line, "openrouter / gpt-test") {
			footer = i
		}
	}
	if composer < 0 || footer < 0 {
		t.Fatalf("view missing composer or footer:\n%s", strings.Join(lines, "\n"))
	}
	if footer <= composer {
		t.Fatalf("footer row %d must render below composer row %d", footer, composer)
	}
	badges := lines[footer]
	route, conv := strings.Index(badges, "openrouter / gpt-test"), strings.Index(badges, "conv-123")
	if conv < route {
		t.Fatalf("footer badges out of order: %q", badges)
	}
	if !strings.Contains(badges, "~200K/1M Tokens") {
		t.Fatalf("footer missing context estimate: %q", badges)
	}
	if !strings.Contains(lines[footer+1], "/ for commands") {
		t.Fatalf("idle hint row = %q, want command hint", lines[footer+1])
	}
}

func TestFooterShowsWorkspaceAndActivePhase(t *testing.T) {
	m := testModel(t)
	root, err := workspace.Open(repoworkspace.NewLocal(), t.TempDir())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	t.Cleanup(func() { _ = root.Close() })
	m.repl.Workspace = root
	m.turnActive = true
	m.phase = "exec write_file"

	footer := ansi.Strip(m.renderFooter(false))
	if !strings.Contains(footer, filepath.Base(root.Path())) {
		t.Fatalf("footer badges = %q, missing workspace", footer)
	}
	status := ansi.Strip(m.renderTurnStatus())
	for _, want := range []string{"write_file", "ctrl+c to interrupt"} {
		if !strings.Contains(status, want) {
			t.Fatalf("turn status = %q, missing %q", status, want)
		}
	}
	if strings.Contains(status, "exec write_file") {
		t.Fatalf("turn status should show the tool name without the phase prefix: %q", status)
	}
}

func TestViewPlacesTurnStatusAboveComposer(t *testing.T) {
	m := testModel(t)
	m.width = 80
	m.textarea.SetWidth(composerInputWidth(80))
	m.turnActive = true
	m.phase = "thinking"
	m.textarea.SetValue("draft")

	lines := strings.Split(ansi.Strip(m.View()), "\n")
	composer, status := -1, -1
	for i, line := range lines {
		if strings.Contains(line, composerPrompt+"draft") {
			composer = i
		}
		if strings.Contains(line, "thinking") {
			status = i
		}
	}
	if composer < 0 || status < 0 {
		t.Fatalf("view missing composer or status:\n%s", strings.Join(lines, "\n"))
	}
	if status >= composer {
		t.Fatalf("status row %d must be above composer row %d", status, composer)
	}
}

func TestWidthChangeRepaintsFromCleanScreen(t *testing.T) {
	m := testModel(t)

	next, cmd := m.Update(tea.WindowSizeMsg{Width: 110, Height: 30})
	if cmd != nil {
		t.Fatal("first size report must not clear the startup banner")
	}
	next, cmd = next.Update(tea.WindowSizeMsg{Width: 110, Height: 20})
	if cmd != nil {
		t.Fatal("height-only change does not reflow rows and must not clear")
	}
	if _, cmd = next.Update(tea.WindowSizeMsg{Width: 70, Height: 20}); cmd == nil {
		t.Fatal("width change must clear the screen before repainting the frame")
	}
}

func TestComposerSurfaceCoversTextareaPadding(t *testing.T) {
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	t.Cleanup(func() { lipgloss.SetColorProfile(profile) })
	m := testModel(t)
	m.width = 40
	m.textarea.SetWidth(composerInputWidth(40))

	surface := composerSurface.Render("x")
	surface = surface[:strings.Index(surface, "x")]
	for line := range strings.Lines(m.renderComposer()) {
		line = strings.TrimSuffix(line, "\n")
		// Any cell written right after a reset must reopen the surface.
		rest := strings.TrimSuffix(line, term.Reset)
		for _, seg := range strings.Split(rest, term.Reset)[1:] {
			if !strings.HasPrefix(seg, surface) {
				t.Fatalf("cells after reset lose the surface color: %q", line)
			}
		}
	}
}

func TestComposerKeepsFirstLineVisibleAfterNewline(t *testing.T) {
	m := testModel(t)
	m.width = 60
	m.textarea.SetWidth(composerInputWidth(60))

	for _, msg := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune("first line")},
		{Type: tea.KeyEnter, Alt: true},
		{Type: tea.KeyRunes, Runes: []rune("second line")},
	} {
		m.editComposer(msg)
		// The textarea only scrolls within content set by its last View.
		_ = m.renderComposer()
	}

	got := ansi.Strip(m.renderComposer())
	for _, want := range []string{composerPrompt + "first line", "second line"} {
		if !strings.Contains(got, want) {
			t.Fatalf("composer = %q, missing %q", got, want)
		}
	}
}

func TestComposerAndFooterFitNarrowWidth(t *testing.T) {
	m := testModel(t)
	m.width = 24
	m.textarea.SetWidth(composerInputWidth(24))
	m.repl.ConversationID = strings.Repeat("c", 60)
	m.repl.Route = strings.Repeat("r", 40)
	m.repl.ModelID = strings.Repeat("m", 40)

	for line := range strings.Lines(m.View()) {
		if w := ansi.StringWidth(strings.TrimSuffix(line, "\n")); w > 24 {
			t.Fatalf("view line exceeds width 24 (got %d): %q", w, line)
		}
	}
}

func TestLiveRendersMarkdownAsChatContent(t *testing.T) {
	m := testModel(t)
	m.turnActive = true

	m.applyEvent(event.NewDeltaEvent("# Hello\n\n**bold** answer"))

	got := m.View()
	if strings.Contains(got, "**bold**") {
		t.Fatalf("assistant markdown leaked as raw syntax: %q", got)
	}
	if !strings.Contains(got, "bold") {
		t.Fatalf("rendered assistant content missing: %q", got)
	}
}

func TestLiveInterleavesToolCallsWithAssistantText(t *testing.T) {
	m := testModel(t)
	call := newTestToolCall(t, "call-1", "write_file", `{"path":"x"}`)

	m.applyEvent(event.NewDeltaEvent("before tool"))
	m.applyEvent(event.NewToolCallEvent(call))
	m.applyEvent(event.NewDeltaEvent("after tool"))

	got := ansi.Strip(m.renderLive())
	before := strings.Index(got, "before tool")
	tool := strings.Index(got, "write_file")
	after := strings.Index(got, "after tool")
	if before < 0 || tool < 0 || after < 0 {
		t.Fatalf("rendered turn missing a segment: %q", got)
	}
	if !(before < tool && tool < after) {
		t.Fatalf("stream order wrong (before=%d tool=%d after=%d): %q", before, tool, after, got)
	}
}

func TestLiveShowsToolCallAndOutput(t *testing.T) {
	m := testModel(t)
	call := newTestToolCall(t, "call-1", "exec", `{"command": "pwd"}`)

	m.applyEvent(event.NewToolCallEvent(call))
	m.applyEvent(event.NewToolOutputEvent(call, "private tool output"))

	got := m.View()
	if !strings.Contains(got, "⏺") || !strings.Contains(got, `{"command": "pwd"}`) || !strings.Contains(got, "private tool output") {
		t.Fatalf("tool activity or output missing: %q", got)
	}
	if strings.Contains(got, "call-1") {
		t.Fatalf("tool id should not render in activity row: %q", got)
	}
}

func TestViewWrapsLiveToTerminalWidth(t *testing.T) {
	m := testModel(t)
	m.width = 20
	m.textarea.SetWidth(20)

	m.applyEvent(event.NewDeltaEvent(strings.Repeat("long ", 20) + "tail"))

	// Every logical line the renderer counts must fit a terminal row —
	// a line that wraps visually makes the next frame under-clear and
	// the status line overwrites live text mid-sentence.
	for line := range strings.Lines(m.View()) {
		if w := ansi.StringWidth(line); w > 20 {
			t.Fatalf("view line exceeds width 20 (got %d): %q", w, line)
		}
	}
}

func TestLiveEscapesTerminalControlSequences(t *testing.T) {
	m := testModel(t)

	m.applyEvent(event.NewDeltaEvent("safe\x1b[2J\rtext"))

	if got, want := m.live.String(), "safe\\u001b[2J\\u000dtext"; got != want {
		t.Fatalf("live terminal text = %q, want %q", got, want)
	}
}

func TestLiveFormatsValidToolCallJSON(t *testing.T) {
	m := testModel(t)
	input := "{\n  \"city\": \"Paris\",\n  \"count\": 2\n}"
	call := newTestToolCall(t, "call-1", "lookup", input)

	m.applyEvent(event.NewToolCallEvent(call))

	got := m.renderLive()
	for _, want := range []string{"⏺", "lookup", input} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered tool call = %q, missing %q", got, want)
		}
	}
	if strings.Contains(got, "call-1") {
		t.Fatalf("tool id should not render in activity row: %q", got)
	}
	if got := call.Input(); got != input {
		t.Fatalf("tool input mutated to %q, want %q", got, input)
	}
}

func TestLivePreservesInvalidToolCallJSON(t *testing.T) {
	m := testModel(t)
	input := `{"query": broken}`
	call := newTestToolCall(t, "call-2", "lookup", input)

	m.applyEvent(event.NewToolCallEvent(call))

	got := m.renderLive()
	if !strings.Contains(got, input) {
		t.Fatalf("rendered tool call = %q, want raw input %q", got, input)
	}
	if strings.Contains(got, "call-2") {
		t.Fatalf("tool id should not render in activity row: %q", got)
	}
	if strings.Contains(got, "\n\n") {
		t.Fatalf("tool input should remain a single activity row, got %q", got)
	}
}

func TestLiveShowsToolOutput(t *testing.T) {
	m := testModel(t)
	first := newTestToolCall(t, "call-1", "lookup", `{}`)

	m.applyEvent(event.NewToolOutputEvent(first, "first"))

	got := m.renderLive()
	if !strings.Contains(got, "first") {
		t.Fatalf("tool output missing from live transcript: %q", got)
	}
}

func TestLivePrintsUsageCounters(t *testing.T) {
	m := testModel(t)
	usage, err := valueobject.NewUsage(11, 3, 2, 17)
	if err != nil {
		t.Fatalf("create usage: %v", err)
	}

	m.applyEvent(event.NewFinishedEvent(enum.FinishReasonStop, usage))

	got := m.renderLive()
	for _, want := range []string{"11 in", "17 out", "33 total", "3 cached read", "2 cached write"} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered usage = %q, missing %q", got, want)
		}
	}
}

func TestLiveIgnoresTurnError(t *testing.T) {
	m := testModel(t)

	m.applyEvent(event.NewErrorEvent(errors.New("already returned by application")))

	if got := m.live.String(); got != "" {
		t.Fatalf("turn error must not render, got %q", got)
	}
}

func newTestToolCall(t *testing.T, id, name, input string) entity.ToolCall {
	t.Helper()
	call, err := entity.NewToolCall(id, name, input, enum.TooltypeOrigin)
	if err != nil {
		t.Fatalf("create tool call: %v", err)
	}
	return call
}
