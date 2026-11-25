package commandinternal

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	appcommand "github.com/jahrulnr/nusallm/application/command"
	"github.com/jahrulnr/nusallm/infrastructure/domain/service"
)

func Conversations(h appcommand.SlashSession, cmd string) func(context.Context, string) []service.CommandCompletion {
	return func(ctx context.Context, arg string) []service.CommandCompletion {
		if strings.IndexFunc(arg, unicode.IsSpace) >= 0 {
			return nil
		}
		conversations, err := h.ListConversations(ctx)
		if err != nil {
			return nil
		}
		titles := DisplayTitles(ctx, h, conversations)
		var rows []service.CommandCompletion
		for _, conv := range conversations {
			displayTitle := titles[conv.ID]
			if !ListDisplayTitleMatchesPrefix(conv.ID, conv.Title, displayTitle, arg) {
				continue
			}
			title := displayTitle
			if runes := []rune(title); len(runes) > 48 {
				title = string(runes[:48]) + "…"
			}
			desc := fmt.Sprintf("%s · %d msgs", title, conv.MessageCount)
			if !conv.UpdatedAt.IsZero() {
				desc += " · " + conv.UpdatedAt.Local().Format("Jan 02 15:04")
			}
			rows = append(rows, service.CommandCompletion{Usage: "/" + cmd + " " + conv.ID, Desc: desc})
			if len(rows) == MaxRows {
				break
			}
		}
		if len(rows) == 0 {
			return nil
		}
		return rows
	}
}
