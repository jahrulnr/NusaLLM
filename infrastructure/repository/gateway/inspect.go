package repogateway

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strings"

	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
)

// Inspect reads the record and hydrates it WITHOUT resolving the stored
// apiKey — the aggregate carries no key material and the raw stored value
// comes back beside it, so the management plane renders records whose
// credential is unresolvable. A file that can't even decode still
// surfaces as a snapshot with nil Gateway rather than hiding; only a
// truly absent id is 404, and genuine I/O failures (permissions, IO
// errors) propagate — they are operational faults, not corruption. The
// snapshot's ID is always the filename — the addressable id — even when
// the record decodes with a mismatched embedded id.
func (r *GatewayRepository) Inspect(_ context.Context, id string) (drepo.GatewaySnapshot, error) {
	if err := validID(id); err != nil {
		return drepo.GatewaySnapshot{}, err
	}
	raw, err := os.ReadFile(r.file(id))
	if errors.Is(err, os.ErrNotExist) {
		return drepo.GatewaySnapshot{}, apperror.NewNotFoundError("gateway "+id, err)
	}
	if err != nil {
		return drepo.GatewaySnapshot{}, err
	}
	var rec gatewayRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		// Corrupt JSON — recover the apiKey ref best-effort so a repair
		// upsert can still clean up the vaulted material it points at.
		var stub struct {
			ApiKey string `json:"apiKey"`
		}
		_ = json.Unmarshal(raw, &stub)
		return drepo.GatewaySnapshot{ID: id, ApiKeyRef: stub.ApiKey}, nil
	}
	g, err := decodeGateway(rec, "")
	if err != nil {
		return drepo.GatewaySnapshot{ID: id, ApiKeyRef: rec.ApiKey}, nil
	}
	return drepo.GatewaySnapshot{ID: id, Gateway: g, ApiKeyRef: rec.ApiKey}, nil
}

// InspectList snapshots every <id>.json in the directory — including
// records Load would fail on — sorted by id for stable ordering.
func (r *GatewayRepository) InspectList(ctx context.Context) ([]drepo.GatewaySnapshot, error) {
	entries, err := os.ReadDir(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []drepo.GatewaySnapshot
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		snap, err := r.Inspect(ctx, strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			// An id that fails validID can't be addressed by any op —
			// the file is only fixable on disk, so skip it. A real I/O
			// failure propagates so `list` doesn't hide a fault.
			if apperror.As(err).StatusCode() == 400 {
				continue
			}
			return nil, err
		}
		out = append(out, snap)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
