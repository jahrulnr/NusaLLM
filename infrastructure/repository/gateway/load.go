package repogateway

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
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
	key, err := r.resolveKey(ctx, id, rec.ApiKey)
	if err != nil {
		return nil, err
	}
	return decodeGateway(rec, key)
}

// resolveKey is the one dispatch over stored apiKey values: "env:NAME"
// reads the environment, "secret:<key>" hits the SecretStore, anything
// else — a literal or "" — passes through verbatim. Load treats a
// failure as fatal; Inspect reads the same decision non-fatally as
// GatewaySnapshot.KeyResolved.
func (r *GatewayRepository) resolveKey(ctx context.Context, id, stored string) (string, error) {
	key := stored
	if name, ok := strings.CutPrefix(key, valueobject.KeyRefEnv); ok {
		if key = os.Getenv(name); key == "" {
			return "", fmt.Errorf("gateway %q: apiKey references env %q which is unset", id, name)
		}
	} else if sk, ok := strings.CutPrefix(key, valueobject.KeyRefSecret); ok {
		if r.secrets == nil {
			return "", fmt.Errorf("gateway %q: apiKey references %q but no secret store is configured", id, sk)
		}
		resolved, err := r.secrets.Get(ctx, sk)
		if err != nil {
			return "", fmt.Errorf("gateway %q: %w", id, err)
		}
		key = resolved
	}
	return key, nil
}

// keyResolves probes the stored value through resolveKey — Inspect's
// resolution flag, same decision Load enforces, read without failing.
func (r *GatewayRepository) keyResolves(ctx context.Context, id, stored string) bool {
	_, err := r.resolveKey(ctx, id, stored)
	return err == nil
}
