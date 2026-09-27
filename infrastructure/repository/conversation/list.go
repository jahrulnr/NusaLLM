package repoconversation

import (
	"context"
	"errors"
	"os"
	"sort"

	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
)

// List reads every conversation's meta and returns infos newest activity
// first. Directories without a readable meta are skipped.
func (r *ConversationRepository) List(ctx context.Context) ([]drepo.ConversationInfo, error) {
	entries, err := os.ReadDir(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []drepo.ConversationInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := r.Info(ctx, e.Name())
		if err != nil {
			continue // skip dirs without a readable meta
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}
