package textcompletion

import (
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
)

// SanitizeCompactedTail drops structured reasoning details from the retained
// tail. Those entries carry provider signatures and encrypted blobs bound to
// the request chain that produced them; compaction changes the prefix, so
// replaying them is not a continuity guarantee. Plain reasoning text remains
// available as the fallback field, and display parts still carry the visible
// summary. The input is not mutated.
func (*Service) SanitizeCompactedTail(tail []entity.Message) []entity.Message {
	out := make([]entity.Message, 0, len(tail))
	for _, m := range tail {
		if _, ok := m.OptionalContent()[reasoningDetailsKey]; ok {
			m = m.WithoutOptionalContent(reasoningDetailsKey)
		}
		out = append(out, m)
	}
	return out
}
