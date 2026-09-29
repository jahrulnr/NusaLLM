package appconversation

import (
	"strings"
	"testing"

	appjob "github.com/jahrulnr/nusallm/application/job"
	"github.com/jahrulnr/nusallm/infrastructure/config"
	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	repoconversation "github.com/jahrulnr/nusallm/infrastructure/repository/conversation"
	repogateway "github.com/jahrulnr/nusallm/infrastructure/repository/gateway"
	reposecret "github.com/jahrulnr/nusallm/infrastructure/repository/secret"
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

	app := New(convs, gateways, map[enum.Environment]appjob.EnvironmentDriver{
		enum.EnvironmentLocal: appjob.EnvironmentBundle{},
	}, config.NewConfig(), nil, "")
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
	if err := conv.SetEnvironment(enum.EnvironmentSSH); err != nil {
		t.Fatal(err)
	}
	if err := convs.Save(t.Context(), conv); err != nil {
		t.Fatal(err)
	}
	if _, err := app.ContextFooterStats(t.Context(), "room", "", ""); err == nil {
		t.Fatal("unavailable driver produced a misleading token estimate")
	}
}

func TestContextFooterStatsUnknownWindow(t *testing.T) {
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

	app := New(convs, gateways, map[enum.Environment]appjob.EnvironmentDriver{
		enum.EnvironmentLocal: appjob.EnvironmentBundle{},
	}, config.NewConfig(), nil, "")
	stats, err := app.ContextFooterStats(t.Context(), "room", "", "")
	if err != nil {
		t.Fatal(err)
	}
	const wantTotal = entity.DefaultUnknownContextWindow - entity.DefaultUnknownMaxOutputTokens
	if stats.Total != wantTotal {
		t.Fatalf("total = %d, want %d", stats.Total, wantTotal)
	}
}
