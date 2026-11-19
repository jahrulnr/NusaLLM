package cmdnew

import (
	"context"
	"fmt"
	"testing"

	appcommand "github.com/jahrulnr/nusallm/application/command"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
)

// newSession embeds the interface so the test only supplies the methods
// /new touches.
type newSession struct {
	appcommand.SlashSession
	id      string
	infoErr error
	route   string
	model   string
}

func (s *newSession) ConversationID() string      { return s.id }
func (s *newSession) SetConversationID(id string) { s.id = id }
func (s *newSession) Route() string               { return s.route }
func (s *newSession) SetRoute(route string)       { s.route = route }
func (s *newSession) ModelID() string             { return s.model }
func (s *newSession) SetModelID(id string)        { s.model = id }
func (s *newSession) ConversationInfo(context.Context, string) (drepo.ConversationInfo, error) {
	return drepo.ConversationInfo{}, s.infoErr
}
func (s *newSession) Write(emitter dservice.CommandEmitter, format string, args ...any) {
	if emitter != nil {
		emitter(fmt.Sprintf(format, args...))
	}
}

// Another tab can delete the active room. /new is the recovery command;
// a missing source room must not trap the user on the dead id just because
// its metadata read fails.
func TestNewFromDeletedActiveRoomStartsFresh(t *testing.T) {
	h := &newSession{id: "deleted", infoErr: apperror.New(404, "conversation not found", nil)}
	if err := New(h).Execute(context.Background(), "", nil); err != nil {
		t.Fatalf("/new from a deleted room: %v", err)
	}
	if h.id != "" {
		t.Fatalf("active id not cleared: %q", h.id)
	}
}
