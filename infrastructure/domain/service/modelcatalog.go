package service

import (
	"context"

	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
)

// ModelCatalog lists model IDs advertised by a configured gateway.
// The adapter owns provider-specific URLs, headers, and pagination.
type ModelCatalog interface {
	ListModels(ctx context.Context, gateway *aggregate.Gateway) ([]string, error)
	// ListProviders returns the upstream providers a gateway can route a
	// model to — aggregator surfaces (OpenRouter-style) expose several;
	// direct providers return empty. Values are {name, slug} pairs the
	// caller echoes back as the model's upstream pin.
	ListProviders(ctx context.Context, gateway *aggregate.Gateway, modelID string) ([]UpstreamProvider, error)
}

// UpstreamProvider is one routable backend behind a gateway surface.
type UpstreamProvider struct {
	Name string // display label, e.g. "DeepInfra"
	Slug string // the value a route pin carries
}
