// Package gateway is the WebUI provider-route boundary — list/get reads
// and the full-replace PUT/DELETE mutations, with the sanitized
// projection (key state, never key material or raw refs) owned here.
package gateway

import (
	"context"

	"github.com/gin-gonic/gin"

	appgateway "github.com/jahrulnr/nusallm/application/gateway"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
)

// Reader is the read surface — the sanitized GatewayInfo projection,
// never a raw record. *appgateway.GatewayApp satisfies it.
type Reader interface {
	Get(ctx context.Context, id string) (appgateway.GatewayInfo, error)
	List(ctx context.Context) ([]appgateway.GatewayInfo, error)
	DiscoverModels(ctx context.Context, id string) ([]string, error)
	DiscoverProviders(ctx context.Context, id, modelID string) ([]dservice.UpstreamProvider, error)
}

// Writer is the mutation surface — full-document upsert, catalog
// re-import, and removal. *appgateway.GatewayApp satisfies it.
type Writer interface {
	Upsert(ctx context.Context, in appgateway.GatewayInput) (appgateway.UpsertOutcome, error)
	ImportModels(ctx context.Context, id string) (appgateway.UpsertOutcome, error)
	Delete(ctx context.Context, id string) error
}

// Deps wires the use cases; Reads and Writes are optional — nil leaves
// the corresponding routes unregistered.
type Deps struct {
	Reads  Reader
	Writes Writer
}

// Register mounts this boundary's routes under /api.
func Register(api *gin.RouterGroup, d Deps) {
	s := &server{reads: d.Reads, writes: d.Writes}
	if d.Reads != nil {
		api.GET("/gateways", s.list)
		api.GET("/gateways/:id", s.get)
		api.GET("/gateways/:id/available-models", s.availableModels)
		api.GET("/gateways/:id/providers", s.providers)
	}
	if d.Writes != nil {
		api.PUT("/gateways/:id", s.put)
		api.POST("/gateways/:id/import-models", s.importModels)
		api.DELETE("/gateways/:id", s.delete)
	}
}

type server struct {
	reads  Reader
	writes Writer
}
