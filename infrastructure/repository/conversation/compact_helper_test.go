package repoconversation

import (
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
)

// compactAt mirrors a production fold: load the live history, hand the
// current tail to Compact, and keep invalid-boundary tests reaching the
// repository's own validation instead of panicking in the helper.
func compactAt(t *testing.T, repo drepo.Conversation, convID string, summary entity.Message, dropBefore int) error {
	t.Helper()
	conv, err := repo.Load(t.Context(), convID)
	if err != nil {
		// A deliberately damaged archive fails to load; still drive the
		// operation under test so Compact's own validation is exercised.
		return repo.Compact(t.Context(), convID, summary, dropBefore, nil)
	}
	msgs := conv.Messages()
	var retained []entity.Message
	if dropBefore >= 0 && dropBefore <= len(msgs) {
		retained = append([]entity.Message(nil), msgs[dropBefore:]...)
	}
	return repo.Compact(t.Context(), convID, summary, dropBefore, retained)
}
