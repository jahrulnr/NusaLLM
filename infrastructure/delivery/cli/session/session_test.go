package session

import (
	"strings"
	"testing"

	appconversation "github.com/jahrulnr/nusallm/application/conversation"
	appgateway "github.com/jahrulnr/nusallm/application/gateway"
	"github.com/jahrulnr/nusallm/infrastructure/config"
	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	repoconversation "github.com/jahrulnr/nusallm/infrastructure/repository/conversation"
	repogateway "github.com/jahrulnr/nusallm/infrastructure/repository/gateway"
	reposecret "github.com/jahrulnr/nusallm/infrastructure/repository/secret"
)

func TestEffectiveRouteModelUsesConversationBinding(t *testing.T) {
	gw, err := aggregate.NewGateway("gw1", "stub", "http://x", enum.StyleCompletion)
	if err != nil {
		t.Fatal(err)
	}
	gw.SetApiKey("sk")
	m, _ := entity.NewModel("m1", "gw1")
	gw.SetModels([]entity.Model{m})
	secrets, err := reposecret.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { secrets.Close() })
	gateways := repogateway.NewGatewayRepository(t.TempDir(), secrets)
	if err := gateways.Save(t.Context(), &gw); err != nil {
		t.Fatal(err)
	}
	convs := repoconversation.NewConversationRepository(t.TempDir())
	conv, err := aggregate.NewConversation("room")
	if err != nil {
		t.Fatal(err)
	}
	conv.SetRoute("gw1")
	conv.SetModelID("m1")
	if err := convs.Save(t.Context(), conv); err != nil {
		t.Fatal(err)
	}

	s := &Session{
		Conversations:  appconversation.New(convs, gateways, nil, config.NewConfig(), nil, "", nil, nil),
		ConversationID: "room",
	}

	route, model := s.EffectiveRouteModel(t.Context())
	if route != "gw1" || model != "m1" {
		t.Fatalf("binding = %q/%q, want gw1/m1", route, model)
	}
}

// The auto-selection decision lives in the gateway app; the session owns
// only the UX — applying the pick to its fields and announcing it.
func TestResolveRouteAutoSelectsAndNotifies(t *testing.T) {
	secrets, err := reposecret.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { secrets.Close() })
	gateways := repogateway.NewGatewayRepository(t.TempDir(), secrets)
	gwApp := appgateway.New(gateways, secrets, nil)
	off := false
	if _, err := gwApp.Upsert(t.Context(), appgateway.GatewayInput{
		ID: "gw1", Name: "n", Style: enum.StyleCompletion, Endpoint: "http://x",
		Stream: &off, Models: []appgateway.ModelInput{{ID: "m1"}},
	}); err != nil {
		t.Fatal(err)
	}
	convs := repoconversation.NewConversationRepository(t.TempDir())

	var notes []string
	s := &Session{
		Conversations: appconversation.New(convs, gateways, nil, config.NewConfig(), nil, "", nil, nil),
		Gateways:      gwApp,
		Notify:        func(text string) { notes = append(notes, text) },
	}
	route, modelID, err := s.ResolveRoute(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if route != "gw1" || modelID != "m1" || s.Route != "gw1" || s.ModelID != "m1" {
		t.Fatalf("resolved %q/%q, session %q/%q — want gw1/m1 bound", route, modelID, s.Route, s.ModelID)
	}
	if len(notes) != 2 || !strings.Contains(notes[0], "auto-selected route gw1") ||
		!strings.Contains(notes[1], "auto-selected model m1") {
		t.Fatalf("notices = %v", notes)
	}
}

// An ambiguous or empty setup is the CLI's question to ask, not the
// app's error — ResolveRoute renders the /route + /model prompt.
func TestResolveRouteUnboundErrors(t *testing.T) {
	secrets, err := reposecret.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { secrets.Close() })
	gateways := repogateway.NewGatewayRepository(t.TempDir(), secrets)
	convs := repoconversation.NewConversationRepository(t.TempDir())
	s := &Session{
		Conversations: appconversation.New(convs, gateways, nil, config.NewConfig(), nil, "", nil, nil),
		Gateways:      appgateway.New(gateways, secrets, nil),
		Notify:        func(string) {},
	}
	if _, _, err := s.ResolveRoute(t.Context()); err == nil ||
		!strings.Contains(err.Error(), "/route and /model") {
		t.Fatalf("unbound route must ask for a selection: %v", err)
	}
}
