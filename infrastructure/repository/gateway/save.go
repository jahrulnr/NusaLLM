package repogateway

import (
	"context"

	"github.com/jahrulnr/llm-base/infrastructure/domain/aggregate"
)

// Save upserts <id>.json atomically via tmp+rename. A literal apiKey is
// auto-stored in the SecretStore first, so the file only carries its
// "secret:" reference.
func (r *GatewayRepository) Save(ctx context.Context, gw *aggregate.Gateway) error {
	if err := validID(gw.ID()); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	rec := encodeGateway(gw)
	key, err := r.encodeKey(ctx, gw.ID(), gw.ApiKey())
	if err != nil {
		return err
	}
	rec.ApiKey = key
	return r.writeGateway(gw.ID(), rec)
}
