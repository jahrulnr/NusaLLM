package appgateway

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
)

type fakeCatalog struct {
	ids []string
	err error
}

func (f fakeCatalog) ListModels(_ context.Context, _ *aggregate.Gateway) ([]string, error) {
	return f.ids, f.err
}

func (f fakeCatalog) ListProviders(_ context.Context, _ *aggregate.Gateway, _ string) ([]dservice.UpstreamProvider, error) {
	return nil, nil
}

// A refresh merges only unknown ids into the record — tuned entries keep
// their parameters, and the apiKey ref survives verbatim (Save writes it
// back, so Inspect must be the read path, not Load's resolved material).
func TestRefreshModelsMergesDiscovered(t *testing.T) {
	app, _, dir := newApp(t)
	in := GatewayInput{
		ID:       "or",
		Style:    enum.StyleCompletion,
		Endpoint: "https://openrouter.ai/api",
		ApiKey:   strptr("sk-or-literal"),
		Models:   []ModelInput{{ID: "a", ContextWindow: 64_000}},
	}
	if err := app.Upsert(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	app.catalog = fakeCatalog{ids: []string{"a", "b", "c"}}

	got := app.RefreshModels(t.Context())
	if len(got) != 1 || got[0].Gateway != "or" || got[0].Error != "" {
		t.Fatalf("result mismatch: %+v", got)
	}
	if len(got[0].Added) != 2 || got[0].Added[0] != "b" || got[0].Added[1] != "c" {
		t.Fatalf("added mismatch: %+v", got[0].Added)
	}

	info, err := app.Get(t.Context(), "or")
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Models) != 3 || info.Models[0].ContextWindow() != 64_000 {
		t.Fatalf("models mismatch: %+v", info.Models)
	}
	raw, _ := os.ReadFile(dir + "/or.json")
	if strings.Contains(string(raw), "sk-or-literal") ||
		!strings.Contains(string(raw), "secret:gateway/or/api_key") {
		t.Fatalf("refresh must not leak key material into the record: %s", raw)
	}

	// Idempotent — a second pass over the same catalog adds nothing.
	got = app.RefreshModels(t.Context())
	if len(got) != 1 || len(got[0].Added) != 0 || got[0].Error != "" {
		t.Fatalf("second pass must be a no-op: %+v", got)
	}
}

// A provider/catalog failure reports per gateway and leaves its record
// untouched — one bad credential never aborts the pass.
func TestRefreshModelsReportsFailures(t *testing.T) {
	app, _, _ := newApp(t)
	for _, in := range []GatewayInput{
		{ID: "ok", Style: enum.StyleCompletion, Endpoint: "https://ok", Models: []ModelInput{{ID: "a"}}},
		{ID: "bad", Style: enum.StyleCompletion, Endpoint: "https://bad"},
	} {
		if err := app.Upsert(t.Context(), in); err != nil {
			t.Fatal(err)
		}
	}
	app.catalog = fakeCatalog{err: errors.New("provider unreachable")}

	got := app.RefreshModels(t.Context())
	if len(got) != 2 {
		t.Fatalf("expected one result per gateway: %+v", got)
	}
	for _, r := range got {
		if r.Error != "provider unreachable" || len(r.Added) != 0 {
			t.Fatalf("result mismatch: %+v", r)
		}
	}
	info, err := app.Get(t.Context(), "ok")
	if err != nil || len(info.Models) != 1 || info.Models[0].ID() != "a" {
		t.Fatalf("failed refresh must not alter the record: %+v", info)
	}
}
