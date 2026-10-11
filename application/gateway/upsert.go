package appgateway

import (
	"context"
	"fmt"
	"strings"

	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
)

// Upsert registers a new gateway or replaces an existing one — the input
// is the whole document, not a patch. Key CRUD rides on the ApiKey
// tri-state: a literal is written to the SecretStore under the canonical
// "gateway/<id>/api_key" name and the record stores only its "secret:"
// reference; an "env:"/"secret:" value persists as-is for callers
// managing their own indirection; "" clears the credential; nil keeps
// whatever is stored, indirection included.
//
// After the record commits, the provider catalog merges into it — the
// same merge the hourly auto-import runs — so a save never waits a tick
// for its models. The import's result is the UpsertOutcome: merged ids
// in Added, a fetch/merge failure in CatalogError. catalog == nil skips
// the import entirely. The failure can never be an Upsert error: the
// record is already written, and an error would have transports retry
// a write that succeeded.
func (app *GatewayApp) Upsert(ctx context.Context, in GatewayInput) (UpsertOutcome, error) {
	if err := app.write(ctx, in); err != nil {
		return UpsertOutcome{}, err
	}
	if app.catalog == nil {
		return UpsertOutcome{}, nil
	}
	added, err := app.importModels(ctx, in.ID)
	if err != nil {
		return UpsertOutcome{CatalogError: err.Error()}, nil
	}
	return UpsertOutcome{Added: added}, nil
}

// write is the record half of Upsert — the whole-document replace plus
// its vault CRUD. It is a separate method because the inspect→save
// window holds app.mu while importModels locks the same mutex, and Go
// mutexes are not reentrant; keeping the provider call out of the lock
// also means catalog latency never serializes management ops.
//
// Write order matters: every input validates before the first store
// call, the vault write precedes the record save (an orphaned value is
// harmless garbage, a dangling ref is a broken gateway), and the
// canonical vault entry is deleted only after the new record stops
// naming it — covering both an explicit clear and a transition to an
// env:/foreign-secret: ref, where the old material would otherwise sit
// in the vault forever.
func (app *GatewayApp) write(ctx context.Context, in GatewayInput) error {
	if _, ok := enum.ParseStyle(in.Style.String()); !ok {
		return apperror.New(400, "invalid gateway",
			fmt.Errorf("unknown style %q", in.Style))
	}
	gw, err := aggregate.NewGateway(in.ID, in.Name, in.Endpoint, in.Style)
	if err != nil {
		return apperror.New(400, "invalid gateway", err)
	}
	if in.Stream != nil && !*in.Stream {
		gw.DisableStream()
	}
	seen := map[string]bool{}
	var models []entity.Model
	for _, mi := range in.Models {
		m, err := app.buildModel(mi)
		if err != nil {
			return err
		}
		// Model(id) resolves first-match — a duplicate id would
		// silently shadow the earlier entry.
		if seen[m.ID()] {
			return apperror.New(400, "invalid gateway",
				fmt.Errorf("duplicate model id %q", m.ID()))
		}
		seen[m.ID()] = true
		models = append(models, m)
	}
	// The stored snapshot drives three things: the key ref on keep, the
	// canonical vault cleanup below, and the server-owned hidden
	// optionalContent each surviving model carries. Transports never see
	// optionalContent, so a full replace would otherwise drop it — a
	// model whose id survives keeps its stored map when the input omits
	// the field (nil), an explicit map overrides, a dropped id loses it
	// with the record.
	//
	// 404 is a first registration — nothing stored; Inspect tolerates a
	// corrupt record too (empty ref, nil gateway), so Upsert repairs a
	// broken file instead of bouncing off it.
	app.mu.Lock()
	defer app.mu.Unlock()
	var oldRef string
	storedOC := map[string]map[string]string{}
	snap, err := app.gateways.Inspect(ctx, in.ID)
	switch {
	case err == nil:
		oldRef = snap.ApiKeyRef
		if snap.Gateway != nil {
			for _, sm := range snap.Gateway.Models() {
				if oc := sm.OptionalContent(); oc != nil {
					storedOC[sm.ID()] = oc
				}
			}
		}
	case apperror.As(err).StatusCode() == apperror.StatusNotFound:
	default:
		return err
	}
	for i := range models {
		if in.Models[i].OptionalContent == nil {
			for k, v := range storedOC[models[i].ID()] {
				models[i].SetOptionalContent(k, v)
			}
		}
	}
	gw.SetModels(models)

	var ref string
	switch {
	case in.ApiKey == nil:
		ref = oldRef
	case *in.ApiKey == "":
		// clear — ref stays empty, the vault entry dies after Save
	case valueobject.IsKeyRef(*in.ApiKey):
		if name, _ := valueobject.KeyRefName(*in.ApiKey); name == "" {
			return apperror.New(400, "invalid gateway",
				fmt.Errorf("apiKey ref %q has an empty name", *in.ApiKey))
		}
		ref = *in.ApiKey
	default:
		if app.secrets == nil {
			return apperror.NewInternalError("gateway key storage unconfigured",
				fmt.Errorf("literal apiKey for %q requires a secret store", in.ID))
		}
		if strings.IndexFunc(*in.ApiKey, func(r rune) bool {
			return r < 0x20 || r == 0x7f
		}) >= 0 {
			return apperror.New(400, "invalid gateway",
				fmt.Errorf("apiKey contains control bytes — it cannot form a valid Authorization header"))
		}
		key := drepo.GatewaySecretKey(in.ID)
		if err := app.secrets.Set(ctx, key, *in.ApiKey); err != nil {
			return err
		}
		ref = valueobject.SecretRef(key)
	}
	gw.SetApiKey(ref)

	if err := app.gateways.Save(ctx, &gw); err != nil {
		return err
	}
	if app.secrets != nil &&
		oldRef == valueobject.SecretRef(drepo.GatewaySecretKey(in.ID)) && ref != oldRef {
		// The record stopped naming the canonical entry — its material
		// is dead weight. A missing entry is already clean.
		if err := app.secrets.Delete(ctx, drepo.GatewaySecretKey(in.ID)); err != nil &&
			apperror.As(err).StatusCode() != apperror.StatusNotFound {
			return err
		}
	}
	return nil
}
