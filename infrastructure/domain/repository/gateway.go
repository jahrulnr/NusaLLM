package repository

import (
	"context"

	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
)

// GatewaySnapshot is the management-plane read of one record: the
// gateway hydrated WITHOUT resolving its apiKey — Gateway is nil when
// the file can't decode at all, so broken records still enumerate
// instead of hiding. ApiKeyRef is the raw stored value — a literal, an
// "env:"/"secret:" ref, or "" — and KeyResolved reports whether that
// value dereferences to usable key material: refs answer for the
// environment/SecretStore they point at, literals and "" resolve by
// construction.
type GatewaySnapshot struct {
	ID          string
	Gateway     *aggregate.Gateway
	ApiKeyRef   string
	KeyResolved bool
}

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
	// through it. apiKey arrives resolved: "env:"/"secret:" refs dereference
	// into key material.
	Load(ctx context.Context, id string) (*aggregate.Gateway, error)

	// Inspect is the management-plane read: the record hydrated with no
	// key material — ApiKeyRef carries the raw stored value (literal,
	// "env:NAME", "secret:<key>", or ""), never resolved, so an update to
	// other fields can carry the ref forward instead of pinning whatever
	// it resolved to. KeyResolved reports whether that stored value
	// resolves — ref resolution is the adapter's rule, read here
	// non-fatally. A record whose credential doesn't resolve still
	// loads; one that can't even decode returns a snapshot with nil
	// Gateway. 404 when absent.
	Inspect(ctx context.Context, id string) (GatewaySnapshot, error)

	// InspectList is List for the management plane: every record appears,
	// including ones Load would fail — an unresolvable key or a corrupt
	// file surfaces as an entry to fix, not a gap in the listing.
	InspectList(ctx context.Context) ([]GatewaySnapshot, error)

	// List returns every configured gateway — backs model pickers and route
	// enumeration.
	List(ctx context.Context) ([]aggregate.Gateway, error)

	// Delete removes a gateway. Conversations keep their route reference —
	// it dangles, and the application owns the fallback when resolution
	// fails.
	Delete(ctx context.Context, id string) error
}
