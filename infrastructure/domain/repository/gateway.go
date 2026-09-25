package repository

import (
	"context"

	"github.com/jahrulnr/llm-base/infrastructure/domain/aggregate"
)

// Gateway is a separate aggregate root persisted on its own — credentials,
// endpoint, and model catalog are managed independently so updates propagate
// to every conversation referencing it by route. Errors carry the apperror
// contract — a missing id surfaces as 404.
//
// Secret material is the implementation's call: apiKey may be persisted
// inline, outsourced to a secret store, or stripped and re-injected from the
// environment at Load — the port does not dictate it.
type Gateway interface {
	// Save upserts the gateway record: style, endpoint, credential, catalog.
	Save(ctx context.Context, gw *aggregate.Gateway) error

	// Load rebuilds one gateway — the resume path resolves Conversation.Route
	// through it.
	Load(ctx context.Context, id string) (*aggregate.Gateway, error)

	// List returns every configured gateway — backs model pickers and route
	// enumeration.
	List(ctx context.Context) ([]aggregate.Gateway, error)

	// Delete removes a gateway. Conversations keep their route reference —
	// it dangles, and the application owns the fallback when resolution
	// fails.
	Delete(ctx context.Context, id string) error
}
