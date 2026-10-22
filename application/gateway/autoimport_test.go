package appgateway

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
)

type fakeCatalog struct {
	ids  []string
	list []dservice.CatalogModel
	err  error
}

func (f fakeCatalog) ListModels(_ context.Context, _ *aggregate.Gateway) ([]dservice.CatalogModel, error) {
	if f.list != nil {
		return f.list, f.err
	}
	out := make([]dservice.CatalogModel, 0, len(f.ids))
	for _, id := range f.ids {
		out = append(out, dservice.CatalogModel{ID: id})
	}
	return out, f.err
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
	if _, err := app.Upsert(t.Context(), in); err != nil {
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

// A refresh backfills provider-declared metadata: unknown fields on an
// existing entry fill in, explicit values stay authoritative, and newly
// discovered ids arrive with their full declared metadata.
func TestRefreshModelsMergesCatalogMetadata(t *testing.T) {
	app, _, _ := newApp(t)
	in := GatewayInput{
		ID:       "or",
		Style:    enum.StyleCompletion,
		Endpoint: "https://openrouter.ai/api",
		Models:   []ModelInput{{ID: "a", ContextWindow: 64_000, Style: enum.StyleMessages}},
	}
	if _, err := app.Upsert(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	app.catalog = fakeCatalog{list: []dservice.CatalogModel{
		{
			ID:               "a",
			Name:             "Model A",
			ContextWindow:    200_000, // declared — but the record's 64K is explicit
			OutputLimit:      32_000,
			SupportedEfforts: []enum.Effort{enum.EffortLow, enum.EffortHigh},
		},
		{
			ID:              "b",
			ContextWindow:   1_000_000,
			AliasOf:         "vendor/base",
			InputModalities: []enum.Modality{enum.ModalityText, enum.ModalityImage},
		},
	}}

	got := app.RefreshModels(t.Context())
	if len(got) != 1 || len(got[0].Added) != 1 || got[0].Added[0] != "b" {
		t.Fatalf("result mismatch: %+v", got)
	}

	info, err := app.Get(t.Context(), "or")
	if err != nil {
		t.Fatal(err)
	}
	a, b := info.Models[0], info.Models[1]
	if a.ContextWindow() != 64_000 {
		t.Fatalf("explicit contextWindow must win over the declaration: %d", a.ContextWindow())
	}
	if a.Name() != "Model A" || a.OutputLimit() != 32_000 ||
		len(a.SupportedEfforts()) != 2 || a.SupportedEfforts()[0] != enum.EffortLow {
		t.Fatalf("undeclared fields must backfill: %+v", a)
	}
	// The wire-style pin is user-set and server-owned — the catalog has
	// no notion of it, so a merge must never clear it.
	if a.Style() != enum.StyleMessages {
		t.Fatalf("merge must preserve the style pin: %q", a.Style())
	}
	if b.ContextWindow() != 1_000_000 || b.AliasOf() != "vendor/base" ||
		len(b.InputModalities()) != 2 || b.InputModalities()[1] != enum.ModalityImage {
		t.Fatalf("new entry must carry declared metadata: %+v", b)
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
		if _, err := app.Upsert(t.Context(), in); err != nil {
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

// A save with the catalog wired merges the provider's ids into the
// record: ids the input already carried keep their parameters, unknown
// ids append, and the apiKey still rides as its "secret:" reference —
// the import path re-saves the record and must never leak resolved
// material into the file.
func TestUpsertImportsCatalog(t *testing.T) {
	app, _, dir := newApp(t)
	app.catalog = fakeCatalog{ids: []string{"a", "b"}}
	in := GatewayInput{
		ID:       "or",
		Style:    enum.StyleCompletion,
		Endpoint: "https://openrouter.ai/api",
		ApiKey:   strptr("sk-or-literal"),
		Models:   []ModelInput{{ID: "a", ContextWindow: 64_000}},
	}
	out, err := app.Upsert(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Added) != 1 || out.Added[0] != "b" || out.CatalogError != "" {
		t.Fatalf("outcome = %+v, want Added [b]", out)
	}
	info, err := app.Get(t.Context(), "or")
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Models) != 2 || info.Models[0].ID() != "a" ||
		info.Models[0].ContextWindow() != 64_000 || info.Models[1].ID() != "b" {
		t.Fatalf("models mismatch: %+v", info.Models)
	}
	raw, _ := os.ReadFile(dir + "/or.json")
	if strings.Contains(string(raw), "sk-or-literal") ||
		!strings.Contains(string(raw), "secret:gateway/or/api_key") {
		t.Fatalf("import must not leak key material into the record: %s", raw)
	}
}

// A catalog failure never fails the write — the record stands with the
// input's models and the outcome carries the fetch failure, so callers
// don't retry a write that already succeeded.
func TestUpsertCatalogErrorKeepsRecord(t *testing.T) {
	app, _, _ := newApp(t)
	app.catalog = fakeCatalog{err: errors.New("provider unreachable")}
	in := GatewayInput{
		ID:       "g",
		Style:    enum.StyleCompletion,
		Endpoint: "https://x",
		Models:   []ModelInput{{ID: "a"}},
	}
	out, err := app.Upsert(t.Context(), in)
	if err != nil {
		t.Fatalf("catalog failure must not fail the write: %v", err)
	}
	if out.CatalogError != "provider unreachable" || len(out.Added) != 0 {
		t.Fatalf("outcome = %+v, want CatalogError only", out)
	}
	info, err := app.Get(t.Context(), "g")
	if err != nil || len(info.Models) != 1 || info.Models[0].ID() != "a" {
		t.Fatalf("catalog failure must not alter the record: %+v, %v", info, err)
	}
}

// No catalog wired means no import — the write still succeeds and no
// provider call happens (there is none to make).
func TestUpsertNilCatalogSkipsImport(t *testing.T) {
	app, _, _ := newApp(t)
	in := GatewayInput{
		ID:       "g",
		Style:    enum.StyleCompletion,
		Endpoint: "https://x",
		Models:   []ModelInput{{ID: "a"}},
	}
	out, err := app.Upsert(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Added) != 0 || out.CatalogError != "" {
		t.Fatalf("nil catalog must yield an empty outcome: %+v", out)
	}
}

// ImportModels splits failure by stage at the app layer: a missing
// gateway propagates its Load 404 — the record itself is absent — while
// a catalog failure after a successful load reports through
// CatalogError because the record stands untouched.
func TestImportModelsErrorSplit(t *testing.T) {
	app, _, _ := newApp(t)
	app.catalog = fakeCatalog{ids: []string{"a"}}

	if _, err := app.ImportModels(t.Context(), "nope"); err == nil ||
		apperror.As(err).StatusCode() != apperror.StatusNotFound {
		t.Fatalf("missing gateway → %v, want 404", err)
	}

	in := GatewayInput{
		ID:       "g",
		Style:    enum.StyleCompletion,
		Endpoint: "https://x",
		Models:   []ModelInput{{ID: "a"}},
	}
	if _, err := app.Upsert(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	app.catalog = fakeCatalog{err: errors.New("provider unreachable")}
	out, err := app.ImportModels(t.Context(), "g")
	if err != nil {
		t.Fatalf("catalog failure must not error the call: %v", err)
	}
	if out.CatalogError != "provider unreachable" || len(out.Added) != 0 {
		t.Fatalf("outcome = %+v, want CatalogError only", out)
	}
	info, err := app.Get(t.Context(), "g")
	if err != nil || len(info.Models) != 1 || info.Models[0].ID() != "a" {
		t.Fatalf("catalog failure must not alter the record: %+v, %v", info, err)
	}
}

// A save's record write and a refresh's inspect→merge→save window hold
// the same mutex, so churn from concurrent Upsert and ImportModels on
// one gateway can interleave only at serialized points — no merged id
// is lost and no call reports an error (a CatalogError outcome would
// still be tolerable; the record is always intact).
func TestImportModelsConcurrentMergeWindow(t *testing.T) {
	app, _, _ := newApp(t)
	app.catalog = fakeCatalog{ids: []string{"a", "b"}}
	in := GatewayInput{
		ID:       "g",
		Style:    enum.StyleCompletion,
		Endpoint: "https://x",
		Models:   []ModelInput{{ID: "a"}},
	}
	if _, err := app.Upsert(t.Context(), in); err != nil {
		t.Fatal(err)
	}

	const goroutines, iterations = 8, 5
	errs := make(chan error, goroutines*iterations)
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				var err error
				if (n+i)%2 == 0 {
					_, err = app.Upsert(t.Context(), in)
				} else {
					_, err = app.ImportModels(t.Context(), "g")
				}
				if err != nil {
					errs <- err
				}
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent op failed: %v", err)
	}

	info, err := app.Get(t.Context(), "g")
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Models) != 2 || info.Models[0].ID() != "a" || info.Models[1].ID() != "b" {
		t.Fatalf("merged record lost an id: %+v", info.Models)
	}
}
