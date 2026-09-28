// Package appgateway is the provider management plane: registrations,
// edits, catalog updates, and removals for the gateways conversations
// route through. It is the sibling of appconversation — that app resolves
// routes to serve turns; this one owns the lifecycle of the routes
// themselves.
//
// Credential rule: key material is write-only here and lives in the
// SecretStore — the gateway record carries only its "secret:<key>" name.
// Upsert performs the vault CRUD itself (set on literal, delete on
// clear); keep-on-update carries the stored ref verbatim so env:/secret:
// indirections survive. Get/List answer a sanitized projection — KeyRef
// exposes the key name, never the resolved material — so transports can
// render "configured" without a key ever crossing the boundary.
package appgateway

import (
	"sync"

	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
)

// GatewayApp fronts the drepo.Gateway port plus the SecretStore the
// records reference by name. Unlike conversations there is no
// per-resource worker — gateways are few, ops are single record writes,
// and the repository already serializes them.
//
// mu serializes each inspect→save read-modify-write (Upsert carries
// stored hidden fields forward; Delete can interleave mid-window). It
// is process-local by nature — a CLI process editing the same store
// concurrently is outside its reach.
type GatewayApp struct {
	gateways drepo.Gateway
	secrets  drepo.SecretStore
	catalog  dservice.ModelCatalog
	mu       sync.Mutex
}

func New(gateways drepo.Gateway, secrets drepo.SecretStore, catalog dservice.ModelCatalog) *GatewayApp {
	return &GatewayApp{gateways: gateways, secrets: secrets, catalog: catalog}
}
