package cmdroute

import (
	"context"
	"fmt"
	"strings"
	"testing"

	appcommand "github.com/jahrulnr/nusallm/application/command"
	appgateway "github.com/jahrulnr/nusallm/application/gateway"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
)

type routeSession struct {
	appcommand.SlashSession
	id      string
	route   string
	model   string
	infoErr error
	out     strings.Builder
}

func (s *routeSession) ConversationID() string { return s.id }
func (s *routeSession) Route() string          { return s.route }
func (s *routeSession) SetRoute(route string)  { s.route = route }
func (s *routeSession) ModelID() string        { return s.model }
func (s *routeSession) SetModelID(id string)   { s.model = id }
func (s *routeSession) ConversationInfo(context.Context, string) (drepo.ConversationInfo, error) {
	return drepo.ConversationInfo{}, s.infoErr
}
func (s *routeSession) ListGateways(context.Context) ([]appgateway.GatewayInfo, error) {
	return []appgateway.GatewayInfo{{ID: "gw1", Name: "Main"}}, nil
}
func (s *routeSession) Write(emitter dservice.CommandEmitter, format string, args ...any) {
	text := fmt.Sprintf(format, args...)
	s.out.WriteString(text)
	if emitter != nil {
		emitter(text)
	}
}

// /route with no argument is the listing/repair view. The active room may
// have been deleted by another tab; that missing metadata must not make
// the configured gateway list unreachable — the user needs it to rebind.
func TestRouteListWorksWhenActiveRoomIsGone(t *testing.T) {
	h := &routeSession{id: "deleted-room", infoErr: apperror.New(404, "conversation not found", nil)}
	if err := New(h).Execute(context.Background(), "", nil); err != nil {
		t.Fatalf("/route list from a deleted room: %v", err)
	}
	if !strings.Contains(h.out.String(), "gw1") {
		t.Fatalf("gateway list missing: %q", h.out.String())
	}
}
