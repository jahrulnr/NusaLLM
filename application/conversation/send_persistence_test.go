package appconversation

import (
	"context"
	"errors"
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
)

func TestSendReportsUserLineCommittedWhenAppendReturnsMetadataError(t *testing.T) {
	app, _ := fixture(t, okHandler)
	stored := app.deps.Convs
	app.deps.Convs = committedAppendErrorRepository{Conversation: stored}

	result, err := app.Send(t.Context(), "room", "gw1", "m1", "hello", nil, nil)
	if err == nil {
		t.Fatal("metadata failure should fail the turn")
	}
	if result.ConversationID != "room" || !result.UserMessagePersisted {
		t.Fatalf("Send result = %+v, want persisted user message", result)
	}
	conv, err := stored.Load(t.Context(), result.ConversationID)
	if err != nil {
		t.Fatalf("committed conversation missing: %v", err)
	}
	if len(conv.Messages()) != 1 || conv.Messages()[0].Content() != "hello" {
		t.Fatalf("committed user message missing: %+v", conv.Messages())
	}
}

type committedAppendErrorRepository struct {
	drepo.Conversation
}

func (r committedAppendErrorRepository) AppendMessage(ctx context.Context, convID string, message entity.Message) (bool, error) {
	persisted, err := r.Conversation.AppendMessage(ctx, convID, message)
	if err != nil || !persisted || message.Role() != enum.RoleUser {
		return persisted, err
	}
	return true, errors.New("metadata projection write failed")
}
