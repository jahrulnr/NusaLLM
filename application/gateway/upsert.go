package appgateway

import (
	"context"
	"fmt"

	"github.com/jahrulnr/llm-base/infrastructure/domain/aggregate"
	"github.com/jahrulnr/llm-base/infrastructure/domain/entity"
	"github.com/jahrulnr/llm-base/infrastructure/domain/enum"
	drepo "github.com/jahrulnr/llm-base/infrastructure/domain/repository"
	"github.com/jahrulnr/llm-base/infrastructure/domain/valueobject"
	apperror "github.com/jahrulnr/llm-base/infrastructure/error"
)

// Upsert registers a new gateway or replaces an existing one — the input
// is the whole document, not a patch. Key CRUD rides on the ApiKey
// tri-state: a literal is written to the SecretStore under the canonical
// "gateway/<id>/api_key" name and the record stores only its "secret:"
// reference; an "env:"/"secret:" value persists as-is for callers
// managing their own indirection; "" clears the credential; nil keeps
// whatever is stored, indirection included.
//
// Write order matters: every input validates before the first store
// call, the vault write precedes the record save (an orphaned value is
// harmless garbage, a dangling ref is a broken gateway), and the
// canonical vault entry is deleted only after the new record stops
// naming it — covering both an explicit clear and a transition to an
// env:/foreign-secret: ref, where the old material would otherwise sit
// in the vault forever.
func (app *GatewayApp) Upsert(ctx context.Context, in GatewayInput) error {
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
	gw.SetModels(models)

	// The stored ref drives keep and cleanup alike: carried verbatim on
	// keep, and an abandoned canonical ref loses its vault entry below.
	// 404 is a first registration — nothing stored; Inspect tolerates a
	// corrupt record too (empty ref), so Upsert repairs a broken file
	// instead of bouncing off it.
	var oldRef string
	snap, err := app.gateways.Inspect(ctx, in.ID)
	switch {
	case err == nil:
		oldRef = snap.ApiKeyRef
	case apperror.As(err).StatusCode() == apperror.StatusNotFound:
	default:
		return err
	}

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
