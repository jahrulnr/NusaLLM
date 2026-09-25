package repoconversation

import (
	"context"
	"errors"
	"os"

	apperror "github.com/jahrulnr/llm-base/infrastructure/error"
)

// Delete removes the whole conversation directory — meta, live tail, and
// every sealed segment.
func (r *ConversationRepository) Delete(ctx context.Context, convID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, err := os.Stat(r.convDir(convID)); errors.Is(err, os.ErrNotExist) {
		return apperror.NewNotFoundError("conversation "+convID, err)
	}
	return os.RemoveAll(r.convDir(convID))
}
