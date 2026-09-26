package appconversation

import (
	"testing"

	"github.com/jahrulnr/llm-base/infrastructure/domain/enum"
	apperror "github.com/jahrulnr/llm-base/infrastructure/error"
)

func TestConversationQueries(t *testing.T) {
	app, _ := fixture(t, okHandler)
	result, err := app.Send(t.Context(), "query-room", "gw1", "m1", "hello", nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	info, err := app.Info(t.Context(), result.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if info.ID != "query-room" || info.MessageCount != 2 {
		t.Fatalf("Info = %+v", info)
	}

	list, err := app.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "query-room" {
		t.Fatalf("List = %+v", list)
	}

	history, err := app.History(t.Context(), result.ConversationID, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].Role() != enum.RoleUser || history[0].Content() != "hello" {
		t.Fatalf("History range = %+v", history)
	}
	last, err := app.History(t.Context(), result.ConversationID, -1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(last) != 1 || last[0].Role() != enum.RoleAssistant || last[0].Content() != "pong" {
		t.Fatalf("History latest = %+v", last)
	}

	if _, err := app.Info(t.Context(), "missing"); apperror.As(err).StatusCode() != 404 {
		t.Fatalf("missing Info → %v, want 404", err)
	}
	if _, err := app.History(t.Context(), "missing", 0, 1); apperror.As(err).StatusCode() != 404 {
		t.Fatalf("missing History → %v, want 404", err)
	}
}
