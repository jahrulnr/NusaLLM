package session

import (
	"testing"

	appconversation "github.com/jahrulnr/llm-base/application/conversation"
	"github.com/jahrulnr/llm-base/infrastructure/config"
	"github.com/jahrulnr/llm-base/infrastructure/domain/aggregate"
	"github.com/jahrulnr/llm-base/infrastructure/domain/entity"
	"github.com/jahrulnr/llm-base/infrastructure/domain/enum"
	repoconversation "github.com/jahrulnr/llm-base/infrastructure/repository/conversation"
	repogateway "github.com/jahrulnr/llm-base/infrastructure/repository/gateway"
	reposecret "github.com/jahrulnr/llm-base/infrastructure/repository/secret"
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
		Conversations:  appconversation.New(convs, gateways, nil, nil, config.NewConfig(), ""),
		ConversationID: "room",
	}

	route, model := s.EffectiveRouteModel(t.Context())
	if route != "gw1" || model != "m1" {
		t.Fatalf("binding = %q/%q, want gw1/m1", route, model)
	}
}
