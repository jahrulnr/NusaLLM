package repogateway

import (
	"context"
	"errors"
	"os"
	"sort"
	"strings"

	drepo "github.com/jahrulnr/llm-base/infrastructure/domain/repository"
	apperror "github.com/jahrulnr/llm-base/infrastructure/error"
)

// Inspect reads the record and hydrates it WITHOUT resolving the stored
// apiKey — the aggregate carries no key material and the raw stored value
// comes back beside it, so the management plane renders records whose
// credential is unresolvable. A file that can't even decode still
// surfaces as a snapshot with nil Gateway rather than hiding; only a
// truly absent id is 404.
func (r *GatewayRepository) Inspect(_ context.Context, id string) (drepo.GatewaySnapshot, error) {
	if err := validID(id); err != nil {
		return drepo.GatewaySnapshot{}, err
	}
	rec, err := r.readGateway(id)
	if err != nil {
		if apperror.As(err).StatusCode() == apperror.StatusNotFound {
			return drepo.GatewaySnapshot{}, err
		}
		return drepo.GatewaySnapshot{ID: id}, nil
	}
	g, err := decodeGateway(rec, "")
	if err != nil {
		return drepo.GatewaySnapshot{ID: id, ApiKeyRef: rec.ApiKey}, nil
	}
	return drepo.GatewaySnapshot{ID: g.ID(), Gateway: g, ApiKeyRef: rec.ApiKey}, nil
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
			// the file is only fixable on disk, so skip it.
			continue
		}
		out = append(out, snap)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
