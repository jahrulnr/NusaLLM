package appgateway

import (
	"context"

	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
)

// DiscoverModels reads the provider catalog using the gateway's current
// endpoint and resolved credential. It leaves the configured catalog alone;
// the caller chooses which IDs to add through Upsert.
func (app *GatewayApp) DiscoverModels(ctx context.Context, id string) ([]string, error) {
	if app.catalog == nil {
		return nil, apperror.NewInternalError("model discovery unavailable", nil)
	}
	gw, err := app.gateways.Load(ctx, id)
	if err != nil {
		return nil, err
	}
	return app.catalog.ListModels(ctx, gw)
}

// DiscoverProviders lists the upstream backends a gateway can route the
// given model to — the choices an aggregator surface exposes to the
// picker's route layer. Direct providers answer an empty list.
func (app *GatewayApp) DiscoverProviders(ctx context.Context, id, modelID string) ([]dservice.UpstreamProvider, error) {
	if app.catalog == nil {
		return nil, apperror.NewInternalError("provider discovery unavailable", nil)
	}
	gw, err := app.gateways.Load(ctx, id)
	if err != nil {
		return nil, err
	}
	return app.catalog.ListProviders(ctx, gw, modelID)
}
