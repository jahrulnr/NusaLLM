package cmddelete

import (
	"context"
	"fmt"
	"testing"

	appcommand "github.com/jahrulnr/nusallm/application/command"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
)

type deleteSession struct {
	appcommand.SlashSession
	id string
}

func (s *deleteSession) ConversationID() string      { return s.id }
func (s *deleteSession) SetConversationID(id string) { s.id = id }
func (s *deleteSession) DeleteConversation(context.Context, string) error {
	return apperror.New(404, "conversation not found", nil)
}
func (s *deleteSession) Write(emitter dservice.CommandEmitter, format string, args ...any) {
	if emitter != nil {
		emitter(fmt.Sprintf(format, args...))
	}
}

// The room may already be gone when /delete lands. The user's intent is
// fulfilled; leaving the active id bound to a deleted room traps every
// later command on a 404. A 404 for the active target clears it.
func TestDeleteAlreadyGoneActiveRoomClearsIt(t *testing.T) {
	h := &deleteSession{id: "gone"}
	if err := New(h).Execute(context.Background(), "", nil); err != nil {
		t.Fatalf("/delete already-gone active room: %v", err)
	}
	if h.id != "" {
		t.Fatalf("active id still %q after idempotent delete", h.id)
	}
}
