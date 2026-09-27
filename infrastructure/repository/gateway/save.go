package repogateway

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
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
	// Unique tmp name — two processes writing the same id concurrently
	// would otherwise truncate each other's staged record into a torn
	// file that reads back as a broken gateway.
	f, err := os.CreateTemp(r.path, gw.ID()+".json.tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op after a successful rename
	_ = f.Chmod(0o600)   // may carry a key ref
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return err
	}
	// fsync before the rename — otherwise a crash can resurrect the
	// record's name with no contents, which reads as a broken gateway.
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, r.file(gw.ID())); err != nil {
		return err
	}
	if d, err := os.Open(r.path); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
