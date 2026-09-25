// Package repogateway implements the Gateway repository port as one JSON
// document per gateway: <path>/<gatewayID>.json. Gateways are few and
// configured rather than user-generated, so a flat file per id keeps Save
// atomic (tmp+rename) and List a directory glob.
//
// apiKey is stored as a reference, not plaintext: "env:VAR_NAME" resolves
// via os.Getenv, "secret:<key>" resolves through the injected SecretStore.
// A literal key passed to Save is auto-stored in the SecretStore when one
// is configured, so the file only ever carries "secret:gateway/<id>/api_key";
// without a store, literals persist as-is for backward compatibility.
package repogateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	drepo "github.com/jahrulnr/llm-base/infrastructure/domain/repository"
	"github.com/jahrulnr/llm-base/infrastructure/domain/valueobject"
	apperror "github.com/jahrulnr/llm-base/infrastructure/error"
)

// GatewayRepository is a JSON-file-backed Gateway store. secrets is the
// injected credential store — nil disables "secret:" resolution and
// literal-key auto-storage. The mutex serializes writes — concurrent Save
// on one id would otherwise collide on the shared .tmp path.
type GatewayRepository struct {
	path    string
	secrets drepo.SecretStore
	mu      sync.Mutex
}

func NewGatewayRepository(path string, secrets drepo.SecretStore) drepo.Gateway {
	return &GatewayRepository{path: path, secrets: secrets}
}

func (r *GatewayRepository) file(id string) string {
	return filepath.Join(r.path, id+".json")
}

// validID is the boundary guard: the id becomes a filename, so the
// domain's own rule (valueobject.ValidID) is re-asserted against this root.
func validID(id string) error {
	if err := valueobject.ValidID(id); err != nil {
		return apperror.New(400, "invalid gateway id", fmt.Errorf("id %q: %w", id, err))
	}
	return nil
}

func (r *GatewayRepository) writeGateway(id string, rec gatewayRecord) error {
	if err := os.MkdirAll(r.path, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	tmp := r.file(id) + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil { // may carry a key
		return err
	}
	return os.Rename(tmp, r.file(id))
}

func (r *GatewayRepository) readGateway(id string) (gatewayRecord, error) {
	raw, err := os.ReadFile(r.file(id))
	if errors.Is(err, os.ErrNotExist) {
		return gatewayRecord{}, apperror.NewNotFoundError("gateway "+id, err)
	}
	if err != nil {
		return gatewayRecord{}, err
	}
	var rec gatewayRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return gatewayRecord{}, fmt.Errorf("gateway %q corrupt: %w", id, err)
	}
	return rec, nil
}

// secretKey is the SecretStore key a gateway's apiKey is filed under when
// a literal is auto-stored at Save.
func secretKey(gatewayID string) string { return "gateway/" + gatewayID + "/api_key" }

// resolveKey dereferences an apiKey record: "env:NAME" reads the
// environment, "secret:<key>" hits the SecretStore, anything else passes
// through as literal key material.
func (r *GatewayRepository) resolveKey(ctx context.Context, v string) (string, error) {
	if name, ok := strings.CutPrefix(v, "env:"); ok {
		if key := os.Getenv(name); key != "" {
			return key, nil
		}
		return "", fmt.Errorf("gateway apiKey references env %q which is unset", name)
	}
	if key, ok := strings.CutPrefix(v, "secret:"); ok {
		if r.secrets == nil {
			return "", fmt.Errorf("gateway apiKey references %q but no secret store is configured", key)
		}
		return r.secrets.Get(ctx, key)
	}
	return v, nil
}

// encodeKey decides what lands in the file: refs stay as-is, literals are
// auto-stored under secretKey(id) when a SecretStore is configured so
// plaintext never reaches disk. Without a store, literals persist as-is.
func (r *GatewayRepository) encodeKey(ctx context.Context, gatewayID, v string) (string, error) {
	if v == "" || r.secrets == nil ||
		strings.HasPrefix(v, "env:") || strings.HasPrefix(v, "secret:") {
		return v, nil
	}
	if err := r.secrets.Set(ctx, secretKey(gatewayID), v); err != nil {
		return "", err
	}
	return "secret:" + secretKey(gatewayID), nil
}
