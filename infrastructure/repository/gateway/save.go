package repogateway

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/jahrulnr/llm-base/infrastructure/domain/aggregate"
	drepo "github.com/jahrulnr/llm-base/infrastructure/domain/repository"
	"github.com/jahrulnr/llm-base/infrastructure/domain/valueobject"
	apperror "github.com/jahrulnr/llm-base/infrastructure/error"
)

// Save upserts <id>.json atomically — tmp+rename, so a crash mid-write
// can't leave a half-written record. The record's apiKey keeps
// "env:"/"secret:" refs verbatim; a literal is vaulted under the
// canonical SecretStore name first so the file only ever carries its
// "secret:" reference — and with no store wired a literal is refused:
// persisting a key into JSON is a deployment defect, not a fallback.
func (r *GatewayRepository) Save(ctx context.Context, gw *aggregate.Gateway) error {
	if err := validID(gw.ID()); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	rec := encodeGateway(gw)
	switch v := gw.ApiKey(); {
	case v == "" || valueobject.IsKeyRef(v):
		rec.ApiKey = v
	case r.secrets == nil:
		return apperror.NewInternalError("gateway key storage unconfigured",
			fmt.Errorf("literal apiKey for %q requires a secret store", gw.ID()))
	default:
		key := drepo.GatewaySecretKey(gw.ID())
		if err := r.secrets.Set(ctx, key, v); err != nil {
			return err
		}
		rec.ApiKey = valueobject.SecretRef(key)
	}

	if err := os.MkdirAll(r.path, 0o700); err != nil { // files may carry key refs
		return err
	}
	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	tmp := r.file(gw.ID()) + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil { // may carry a key
		return err
	}
	return os.Rename(tmp, r.file(gw.ID()))
}
