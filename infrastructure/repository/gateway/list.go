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
// ordering. Record-level problems — a vanished file, an unaddressable id,
// undecodable or unresolvable data — are skipped rather than failing the
// listing; a genuine I/O fault (the file exists but can't be read)
// propagates so a gateway never silently hides behind an operational fault.
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
			if errors.Is(err, errUnreadable) {
				return nil, err
			}
			continue
		}
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })
	return out, nil
}
