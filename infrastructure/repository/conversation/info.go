package repoconversation

import (
	"context"

	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
)

// Info projects meta.json — O(1), no message parsing.
func (r *ConversationRepository) Info(ctx context.Context, convID string) (drepo.ConversationInfo, error) {
	if err := validID(convID); err != nil {
		return drepo.ConversationInfo{}, err
	}
	meta, err := r.readMeta(convID)
	if err != nil {
		return drepo.ConversationInfo{}, err
	}
	return drepo.ConversationInfo{
		ID:             meta.ID,
		Title:          meta.Title,
		Route:          meta.Route,
		ModelID:        meta.ModelID,
		Workspace:      meta.Workspace,
		Usage:          mustUsage(meta.Usage),
		MessageCount:   meta.MessageCount,
		ActiveCount:    meta.ActiveCount,
		SealedSegments: meta.SealedSegments,
		CreatedAt:      meta.CreatedAt,
		UpdatedAt:      meta.UpdatedAt,
	}, nil
}
