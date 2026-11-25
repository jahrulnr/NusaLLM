package cmdmodel

import (
	"context"
	"fmt"
	"strings"
	"testing"

	appcommand "github.com/jahrulnr/nusallm/application/command"
	appgateway "github.com/jahrulnr/nusallm/application/gateway"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
)

type modelSession struct {
	appcommand.SlashSession
	id      string
	route   string
	model   string
	infoErr error
	gw      appgateway.GatewayInfo
	out     strings.Builder
}

func (s *modelSession) ConversationID() string { return s.id }
func (s *modelSession) Route() string          { return s.route }
func (s *modelSession) ModelID() string        { return s.model }
func (s *modelSession) SetModelID(id string)   { s.model = id }
func (s *modelSession) ConversationInfo(context.Context, string) (drepo.ConversationInfo, error) {
	return drepo.ConversationInfo{}, s.infoErr
}
func (s *modelSession) GetGateway(context.Context, string) (appgateway.GatewayInfo, error) {
	return s.gw, nil
}
func (s *modelSession) Write(emitter dservice.CommandEmitter, format string, args ...any) {
	text := fmt.Sprintf(format, args...)
	s.out.WriteString(text)
	if emitter != nil {
		emitter(text)
	}
}

// A route can already be selected while the active room was deleted in
// another tab. /model with no argument is a listing command for that
// route; it does not need the missing room's metadata to enumerate the
// catalog.
func TestModelListWorksWithoutActiveRoomMetadata(t *testing.T) {
	m, _ := entity.NewModel("m1", "")
	m.SetContextWindow(200_000)
	h := &modelSession{
		id: "deleted-room", route: "gw1", infoErr: apperror.New(404, "conversation not found", nil),
		gw: appgateway.GatewayInfo{ID: "gw1", Models: []entity.Model{m}},
	}
	if err := New(h).Execute(context.Background(), "", nil); err != nil {
		t.Fatalf("/model list with a deleted room but a known route: %v", err)
	}
	if !strings.Contains(h.out.String(), "m1") {
		t.Fatalf("model list missing: %q", h.out.String())
	}
}
