package appgateway

import (
	"context"
	"time"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
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
	// A nil report means the caller wants the refresh without
	// diagnostics. The first pass runs before the select, so a
	// cancelled ctx does not skip it — the guard has to be on the call.
	emit := func(r AutoImportResult) {
		if report != nil {
			report(r)
		}
	}
	for _, r := range app.RefreshModels(ctx) {
		emit(r)
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			for _, r := range app.RefreshModels(ctx) {
				emit(r)
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

// importModels fetches the provider catalog for one gateway and merges it
// into the record: unknown ids append, known ids backfill the metadata
// fields they leave unknown. The provider call happens outside mu (network
// latency must not serialize management ops); the inspect→merge→save window
// holds it so a concurrent Upsert can't clobber the merged set. The merged
// record goes through Save, which writes apiKey verbatim — safe only
// because Inspect's snapshot carries the raw ref, never resolved material.
func (app *GatewayApp) importModels(ctx context.Context, id string) ([]string, error) {
	// The ref is the stored credential identity. Load resolves it, so a
	// later compare of Gateway.ApiKey alone cannot see that the record
	// still names the same "secret:" slot.
	before, err := app.gateways.Inspect(ctx, id)
	if err != nil {
		return nil, err
	}
	loaded, err := app.gateways.Load(ctx, id)
	if err != nil {
		return nil, err
	}
	listCtx, cancel := context.WithTimeout(ctx, catalogListTimeout)
	catalog, err := app.catalog.ListModels(listCtx, loaded)
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
	// Resolved material catches a rotation that keeps the same secret
	// ref: the vault value changed, the stored "secret:" string did not.
	now, err := app.gateways.Load(ctx, id)
	if err != nil {
		return nil, err
	}
	// The catalog was fetched against the record as it was — an Upsert
	// that replaced the endpoint, the style, or the credential since is
	// a different provider surface, and its models would land on a
	// gateway they cannot serve.
	if snap.Gateway.Endpoint() != loaded.Endpoint() || snap.Gateway.Style() != loaded.Style() ||
		snap.ApiKeyRef != before.ApiKeyRef || now.ApiKey() != loaded.ApiKey() {
		return nil, nil
	}
	models := snap.Gateway.Models()
	index := make(map[string]int, len(models))
	for i, m := range models {
		index[m.ID()] = i
	}
	var added []string
	changed := false
	for _, cm := range catalog {
		if i, ok := index[cm.ID]; ok {
			if applyCatalogModel(&models[i], cm, snap.Gateway.Style()) {
				changed = true
			}
			continue
		}
		m, err := entity.NewModel(cm.ID, "")
		if err != nil {
			continue // a malformed provider id is not fatal to the pass
		}
		applyCatalogModel(&m, cm, snap.Gateway.Style())
		models = append(models, m)
		added = append(added, cm.ID)
		index[cm.ID] = len(models) - 1
		changed = true
	}
	if !changed {
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

// applyCatalogModel backfills provider-declared metadata onto a stored
// entry: a field fills only while the record leaves it unknown, so an
// explicit value — user-set or previously declared — stays authoritative.
// Clearing a field to zero on Upsert re-derives it at the next import.
// The style pin follows the same rule: the catalog's recommended API fills
// an unset pin, but only when it differs from the gateway's style — a pin
// equal to the gateway's own style would just freeze today's default.
// Returns whether anything changed.
func applyCatalogModel(m *entity.Model, cm dservice.CatalogModel, gwStyle enum.Style) bool {
	changed := false
	if m.Style() == "" && cm.Style != "" && cm.Style != gwStyle {
		m.SetStyle(cm.Style)
		changed = true
	}
	if m.Name() == "" && cm.Name != "" {
		m.SetName(cm.Name)
		changed = true
	}
	if m.Canonical() == "" && cm.Canonical != "" {
		m.SetCanonical(cm.Canonical)
		changed = true
	}
	if m.AliasOf() == "" && cm.AliasOf != "" {
		m.SetAliasOf(cm.AliasOf)
		changed = true
	}
	// The request cap (maxOutputTokens) is a third side of the same
	// triangle the write path validates: a window or ceiling below it
	// pairs into a request the provider can never honor — skip the
	// claim that would create it.
	maxOut := 0
	if v := m.MaxOutputTokens(); v != nil {
		maxOut = *v
	}
	if m.ContextWindow() == 0 && cm.ContextWindow > 0 {
		// A window smaller than the declared output ceiling would pair
		// into an impossible request — skip a claim that conflicts with
		// what the record already says.
		if (m.OutputLimit() == 0 || cm.ContextWindow >= m.OutputLimit()) &&
			(maxOut == 0 || cm.ContextWindow >= maxOut) {
			m.SetContextWindow(cm.ContextWindow)
			changed = true
		}
	}
	if m.OutputLimit() == 0 && cm.OutputLimit > 0 {
		if (m.ContextWindow() == 0 || cm.OutputLimit <= m.ContextWindow()) &&
			(maxOut == 0 || cm.OutputLimit >= maxOut) {
			m.SetOutputLimit(cm.OutputLimit)
			changed = true
		}
	}
	if m.ModelType() == "" && cm.ModelType != "" {
		m.SetModelType(cm.ModelType)
		changed = true
	}
	if len(m.InputModalities()) == 0 && len(cm.InputModalities) > 0 {
		m.SetInputModalities(cm.InputModalities)
		changed = true
	}
	if len(m.OutputModalities()) == 0 && len(cm.OutputModalities) > 0 {
		m.SetOutputModalities(cm.OutputModalities)
		changed = true
	}
	if len(m.SupportedEfforts()) == 0 && len(cm.SupportedEfforts) > 0 {
		m.SetSupportedEfforts(cm.SupportedEfforts)
		changed = true
	}
	if m.Pricing() == (valueobject.Pricing{}) && cm.Pricing != nil {
		m.SetPricing(*cm.Pricing)
		changed = true
	}
	return changed
}
