package appgateway

import (
	"context"
	"time"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
)

// catalogListTimeout bounds one provider catalog fetch inside a
// management call. The catalog client's own http.Client deadline stays
// the outer ceiling, but a hung provider must not hold a gateway save
// or an import pass hostage for that long.
const catalogListTimeout = 15 * time.Second

// AutoImportResult reports one gateway's refresh outcome to the caller's
// diagnostics sink. Added lists newly imported model ids; Error carries a
// per-gateway failure — one bad credential never aborts the pass.
type AutoImportResult struct {
	Gateway string   `json:"gateway"`
	Added   []string `json:"added,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// StartAutoImport refreshes every gateway's model catalog immediately and
// then on interval until ctx ends. Newly discovered ids merge into the
// stored record so the picker's catalog stays current without a manual
// import; existing entries keep their tuned parameters, and the record's
// apiKey ref rides along untouched — Inspect hydrates it as a reference,
// never as resolved material, so a refresh can never write a key into a
// gateway file.
func (app *GatewayApp) StartAutoImport(ctx context.Context, interval time.Duration, report func(AutoImportResult)) {
	if app.catalog == nil || interval <= 0 {
		return
	}
	for _, r := range app.RefreshModels(ctx) {
		report(r)
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			for _, r := range app.RefreshModels(ctx) {
				report(r)
			}
		}
	}
}

// RefreshModels runs a single import pass over every stored gateway and
// returns a result per gateway attempted. A gateway whose record cannot
// decode (Inspect tolerates it as a nil-Gateway stub), whose key cannot
// resolve, or whose provider rejects the catalog call reports an Error and
// leaves its record untouched.
func (app *GatewayApp) RefreshModels(ctx context.Context) []AutoImportResult {
	if app.catalog == nil {
		return nil
	}
	snaps, err := app.gateways.InspectList(ctx)
	if err != nil {
		return []AutoImportResult{{Error: err.Error()}}
	}
	var results []AutoImportResult
	for _, snap := range snaps {
		if snap.Gateway == nil {
			continue // corrupt record — nothing safe to merge into
		}
		r := AutoImportResult{Gateway: snap.Gateway.ID()}
		if added, err := app.importModels(ctx, snap.Gateway.ID()); err != nil {
			r.Error = err.Error()
		} else {
			r.Added = added
		}
		results = append(results, r)
	}
	return results
}

// ImportModels reruns the catalog merge for one stored gateway without
// the Upsert write first — the refresh path behind the import-models
// endpoint. A missing record propagates its 404 (the gateway itself is
// absent, not the catalog); every other import-side failure reports
// through CatalogError because the record is intact. catalog == nil is
// a wiring error.
func (app *GatewayApp) ImportModels(ctx context.Context, id string) (UpsertOutcome, error) {
	if app.catalog == nil {
		return UpsertOutcome{}, apperror.NewInternalError("model import unavailable", nil)
	}
	added, err := app.importModels(ctx, id)
	if err != nil {
		if apperror.As(err).StatusCode() == apperror.StatusNotFound {
			return UpsertOutcome{}, err
		}
		return UpsertOutcome{CatalogError: err.Error()}, nil
	}
	return UpsertOutcome{Added: added}, nil
}

// importModels fetches the provider catalog for one gateway and appends
// unknown ids to its record. The provider call happens outside mu (network
// latency must not serialize management ops); the inspect→merge→save window
// holds it so a concurrent Upsert can't clobber the merged set. The merged
// record goes through Save, which writes apiKey verbatim — safe only
// because Inspect's snapshot carries the raw ref, never resolved material.
func (app *GatewayApp) importModels(ctx context.Context, id string) ([]string, error) {
	loaded, err := app.gateways.Load(ctx, id)
	if err != nil {
		return nil, err
	}
	listCtx, cancel := context.WithTimeout(ctx, catalogListTimeout)
	ids, err := app.catalog.ListModels(listCtx, loaded)
	cancel()
	if err != nil {
		return nil, err
	}
	app.mu.Lock()
	defer app.mu.Unlock()
	snap, err := app.gateways.Inspect(ctx, id)
	if err != nil {
		return nil, err
	}
	if snap.Gateway == nil {
		return nil, nil // became unreadable between list and merge — skip
	}
	models := snap.Gateway.Models()
	seen := make(map[string]bool, len(models))
	for _, m := range models {
		seen[m.ID()] = true
	}
	var added []string
	for _, mid := range ids {
		if seen[mid] {
			continue
		}
		m, err := entity.NewModel(mid, "")
		if err != nil {
			continue // a malformed provider id is not fatal to the pass
		}
		models = append(models, m)
		added = append(added, mid)
		seen[mid] = true
	}
	if len(added) == 0 {
		return nil, nil
	}
	snap.Gateway.SetModels(models)
	// Inspect hydrates with an empty apiKey — the record's credential field
	// only survives in ApiKeyRef. Reattach the raw ref verbatim before Save
	// or the refresh would wipe the "secret:"/"env:" indirection — and never
	// Load's resolved material, which Save would persist as plaintext.
	snap.Gateway.SetApiKey(snap.ApiKeyRef)
	if err := app.gateways.Save(ctx, snap.Gateway); err != nil {
		return nil, err
	}
	return added, nil
}
