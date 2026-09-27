package repogateway

import (
	"context"
	"errors"
	"os"
	"sort"
	"strings"

	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
)

// List hydrates every <id>.json in the directory, sorted by id for stable
// ordering. Corrupt files are skipped rather than failing the listing.
func (r *GatewayRepository) List(ctx context.Context) ([]aggregate.Gateway, error) {
	entries, err := os.ReadDir(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []aggregate.Gateway
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		g, err := r.Load(ctx, strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue
		}
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })
	return out, nil
}
