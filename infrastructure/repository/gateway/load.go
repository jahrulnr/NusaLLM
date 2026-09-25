package repogateway

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/jahrulnr/llm-base/infrastructure/domain/aggregate"
	"github.com/jahrulnr/llm-base/infrastructure/domain/valueobject"
)

// Load reads <id>.json and hydrates the aggregate. The apiKey record
// dereferences here so the file itself stays credential-free: "env:NAME"
// reads the environment, "secret:<key>" hits the SecretStore, anything
// else passes through as literal key material. An unset env var fails
// the load rather than resolving to "" — a silent empty key becomes an
// upstream 401.
func (r *GatewayRepository) Load(ctx context.Context, id string) (*aggregate.Gateway, error) {
	if err := validID(id); err != nil {
		return nil, err
	}
	rec, err := r.readGateway(id)
	if err != nil {
		return nil, err
	}
	key := rec.ApiKey
	if name, ok := strings.CutPrefix(key, valueobject.KeyRefEnv); ok {
		if key = os.Getenv(name); key == "" {
			return nil, fmt.Errorf("gateway %q: apiKey references env %q which is unset", id, name)
		}
	} else if sk, ok := strings.CutPrefix(key, valueobject.KeyRefSecret); ok {
		if r.secrets == nil {
			return nil, fmt.Errorf("gateway %q: apiKey references %q but no secret store is configured", id, sk)
		}
		if key, err = r.secrets.Get(ctx, sk); err != nil {
			return nil, fmt.Errorf("gateway %q: %w", id, err)
		}
	}
	return decodeGateway(rec, key)
}
