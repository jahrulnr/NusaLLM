package service

import (
	"context"

	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

// CatalogModel is one entry a gateway advertises, carrying the metadata
// the provider declares for it. Zero values mean "the provider did not
// say" — merge logic fills unknown fields, it never clears set ones.
type CatalogModel struct {
	ID               string
	Name             string // display name the provider publishes
	Canonical        string // stable base id, e.g. a canonical slug
	AliasOf          string // target id when the entry is a pure alias
	ContextWindow    int    // total context window in tokens
	OutputLimit      int    // max completion tokens
	InputModalities  []enum.Modality
	OutputModalities []enum.Modality
	SupportedEfforts []enum.Effort
	Pricing          *valueobject.Pricing // per-1M USD rates, nil = undeclared
}

// ModelCatalog lists model entries advertised by a configured gateway.
// The adapter owns provider-specific URLs, headers, pagination, and how
// much metadata the surface declares — a thin /models list yields id-only
// entries. The wire id is never rewritten: Canonical/AliasOf describe the
// entry, callers keep sending ID verbatim.
type ModelCatalog interface {
	ListModels(ctx context.Context, gateway *aggregate.Gateway) ([]CatalogModel, error)
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
