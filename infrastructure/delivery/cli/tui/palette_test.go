package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/delivery/cli/commands"
)

func testPalette() *slashPalette {
	infos := []commands.Info{
		{Usage: "/exit", Desc: "leave"},
		{Usage: "/help", Desc: "commands"},
		{Usage: "/history [count]", Desc: "messages"},
		{Usage: "/resume <id>", Desc: "resume"},
	}
	return &slashPalette{describe: func() []commands.Info { return infos }}
}

func TestPaletteOpensOnBareSlashToken(t *testing.T) {
	p := testPalette()
	if rows := p.rows(t.Context(), "/"); len(rows) != 4 {
		t.Fatalf("bare slash must list all commands, got %d", len(rows))
	}
	if rows := p.rows(t.Context(), "/h"); len(rows) != 2 {
		t.Fatalf("prefix /h must match help+history, got %d", len(rows))
	}
	if rows := p.rows(t.Context(), "/zzz"); rows != nil {
		t.Fatalf("unknown token must close palette, got %v", rows)
	}
}

func TestPaletteIgnoresNonSlashAndArgText(t *testing.T) {
	p := testPalette() // no completer — argument positions stay closed
	if rows := p.rows(t.Context(), "hello"); rows != nil {
		t.Fatal("plain text must not open the palette")
	}
	if rows := p.rows(t.Context(), "/resume abc"); rows != nil {
		t.Fatal("args with no completer must not open the palette")
	}
	if rows := p.rows(t.Context(), "/resume\nabc"); rows != nil {
		t.Fatal("multi-line input must not open the palette")
	}
}

func TestPaletteCompletesArguments(t *testing.T) {
	p := &slashPalette{
		complete: func(_ context.Context, buffer string) []commands.Info {
			_, arg, _ := strings.Cut(buffer, " ")
			var rows []commands.Info
			for _, id := range []string{"c_alpha", "c_beta"} {
				if strings.HasPrefix(id, strings.TrimSpace(arg)) {
					rows = append(rows, commands.Info{Usage: "/resume " + id, Desc: "chat"})
				}
			}
			return rows
		},
	}
	rows := p.rows(t.Context(), "/resume ")
	if len(rows) != 2 || !p.arg {
		t.Fatalf("/resume <space> rows = %v arg=%v, want both conversations", rows, p.arg)
	}
	if rows := p.rows(t.Context(), "/resume c_b"); len(rows) != 1 || rows[0].Usage != "/resume c_beta" {
		t.Fatalf("arg prefix filter = %v, want /resume c_beta", rows)
	}
	// Esc closes until the buffer changes again.
	p.dismiss("/resume c_b")
	if rows := p.rows(t.Context(), "/resume c_b"); rows != nil {
		t.Fatal("dismissed arg buffer must stay closed")
	}
	if rows := p.rows(t.Context(), "/resume c_be"); len(rows) != 1 {
		t.Fatal("editing the buffer must reopen the palette")
	}
}

func TestPaletteDismissUntilTokenChanges(t *testing.T) {
	p := testPalette()
	p.dismiss("/h")
	if rows := p.rows(t.Context(), "/h"); rows != nil {
		t.Fatal("dismissed token must stay closed")
	}
	if rows := p.rows(t.Context(), "/he"); len(rows) != 1 {
		t.Fatal("a changed token must reopen the palette")
	}
}

func TestPaletteNavigationWraps(t *testing.T) {
	p := testPalette()
	rows := p.rows(t.Context(), "/h")
	p.move(-1, rows)
	if p.selected != 1 {
		t.Fatalf("wrap backwards = %d, want 1", p.selected)
	}
	p.move(1, rows)
	if p.selected != 0 {
		t.Fatalf("wrap forwards = %d, want 0", p.selected)
	}
	if got := selectedName(rows, p.selected); got != "/help" {
		t.Fatalf("selectedName = %q, want /help", got)
	}
}
