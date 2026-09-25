package repogateway

import (
	"context"
	"fmt"

	"github.com/jahrulnr/llm-base/infrastructure/domain/aggregate"
)

// Load reads <id>.json and hydrates the aggregate — "env:"/"secret:"
// apiKey refs resolve here, so the file itself stays credential-free.
func (r *GatewayRepository) Load(ctx context.Context, id string) (*aggregate.Gateway, error) {
	if err := validID(id); err != nil {
		return nil, err
	}
	rec, err := r.readGateway(id)
	if err != nil {
		return nil, err
	}
	key, err := r.resolveKey(ctx, rec.ApiKey)
	if err != nil {
		return nil, fmt.Errorf("gateway %q: %w", id, err)
	}
	return decodeGateway(rec, key)
}
