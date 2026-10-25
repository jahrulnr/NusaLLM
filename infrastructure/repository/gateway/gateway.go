// Package repogateway implements the Gateway repository port as one JSON
// document per gateway: <path>/<gatewayID>.json. Gateways are few and
// configured rather than user-generated, so a flat file per id keeps Save
// atomic (tmp+rename) and List a directory glob.
//
// apiKey is stored as a reference, not plaintext: "env:VAR_NAME" resolves
// via os.Getenv, "secret:<key>" resolves through the injected SecretStore.
// A literal key passed to Save is auto-stored in the SecretStore when one
// is configured, so the file only ever carries "secret:gateway/<id>/api_key";
// without a store, a literal is refused — persisting a key into JSON is a
// deployment defect, not a fallback.
package repogateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
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

// errUnreadable marks an existing record the filesystem refused to read —
// a permission or I/O fault, not data damage. Listings propagate it so a
// gateway can never silently vanish behind an operational fault.
var errUnreadable = errors.New("gateway record unreadable")

func (r *GatewayRepository) readGateway(id string) (gatewayRecord, error) {
	raw, err := os.ReadFile(r.file(id))
	if errors.Is(err, os.ErrNotExist) {
		return gatewayRecord{}, apperror.NewNotFoundError("gateway "+id, err)
	}
	if err != nil {
		return gatewayRecord{}, fmt.Errorf("%w: %v", errUnreadable, err)
	}
	var rec gatewayRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return gatewayRecord{}, fmt.Errorf("gateway %q corrupt: %w", id, err)
	}
	return rec, nil
}
