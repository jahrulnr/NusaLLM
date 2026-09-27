package appconversation

import (
	"strings"
	"testing"

	"github.com/jahrulnr/llm-base/infrastructure/config"
	"github.com/jahrulnr/llm-base/infrastructure/domain/aggregate"
	"github.com/jahrulnr/llm-base/infrastructure/domain/entity"
	"github.com/jahrulnr/llm-base/infrastructure/domain/enum"
	repoconversation "github.com/jahrulnr/llm-base/infrastructure/repository/conversation"
	repogateway "github.com/jahrulnr/llm-base/infrastructure/repository/gateway"
	reposecret "github.com/jahrulnr/llm-base/infrastructure/repository/secret"
)

func TestContextFooterStats(t *testing.T) {
	gw, err := aggregate.NewGateway("gw1", "stub", "http://x", enum.StyleCompletion)
	if err != nil {
		t.Fatal(err)
	}
	gw.SetApiKey("sk")
	m, _ := entity.NewModel("m1", "gw1")
	m.SetContextWindow(1_000_000)
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
	user, _ := entity.NewMessage(enum.RoleUser, strings.Repeat("x", 800_000))
	if _, err := convs.AppendMessage(t.Context(), "room", user); err != nil {
		t.Fatal(err)
	}

	app := New(convs, gateways, nil, nil, config.NewConfig())
	stats, err := app.ContextFooterStats(t.Context(), "room", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if stats.Total != 1_000_000 {
		t.Fatalf("total = %d", stats.Total)
	}
	if stats.Estimate < 190_000 {
		t.Fatalf("estimate = %d, want ~200k from chars/4", stats.Estimate)
	}
}
