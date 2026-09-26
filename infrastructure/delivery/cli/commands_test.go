package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/jahrulnr/llm-base/infrastructure/domain/service"
)

func TestCommandCompleterSuggestsSlashCommands(t *testing.T) {
	handler := commandFunc(func(context.Context, service.CommandEmitter, string) error { return nil })
	commands := &commandRegistry{commands: map[string]service.CommandService{
		"help":    handler,
		"history": handler,
		"route":   handler,
	}}
	candidates, replaced := (commandCompleter{commands: commands}).Do([]rune("/hel"), len([]rune("/hel")))
	if replaced != 4 || len(candidates) != 1 || string(candidates[0]) != "p" {
		t.Fatalf("completion = %q, replace=%d; want [p], replace=4", candidates, replaced)
	}
	if candidates, _ := (commandCompleter{commands: commands}).Do([]rune("hello"), len([]rune("hello"))); len(candidates) != 0 {
		t.Fatalf("ordinary text should not complete commands: %q", candidates)
	}
	if strings.Join(commands.Suggestions(), ",") != "/help,/history,/route" {
		t.Fatalf("suggestions not sorted: %v", commands.Suggestions())
	}
}

func TestAttachmentPathAcceptsQuotedSpaces(t *testing.T) {
	if got, err := attachmentPath(`"images/a photo.png"`); err != nil || got != "images/a photo.png" {
		t.Fatalf("attachment path = %q, %v", got, err)
	}
	if _, err := attachmentPath(`"unterminated`); err == nil {
		t.Fatal("invalid quoted attachment path should fail")
	}
}

func TestSplitFirstPreservesReplacementSpacing(t *testing.T) {
	id, replacement, ok := splitFirst("  m_123  keep  these spaces  ")
	if !ok || id != "m_123" || replacement != "keep  these spaces" {
		t.Fatalf("splitFirst = %q %q %t", id, replacement, ok)
	}
}
