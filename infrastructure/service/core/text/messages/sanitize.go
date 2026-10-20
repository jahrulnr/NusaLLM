package textmessages

import (
	"encoding/json"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
)

// SanitizeCompactedTail drops thinking and redacted_thinking blocks from
// the retained assistant turns. Compaction rewrites the prefix those
// signatures are bound to, so replaying them would make Claude reject the
// next request; text and tool_use placeholders remain, and the adapter's
// default block order rebuilds the turn without reasoning. The input is not
// mutated.
func (*Service) SanitizeCompactedTail(tail []entity.Message) []entity.Message {
	out := make([]entity.Message, 0, len(tail))
	for _, m := range tail {
		raw := m.OptionalContent()[blocksKey]
		if raw == "" {
			out = append(out, m)
			continue
		}
		var blocks []json.RawMessage
		if err := json.Unmarshal([]byte(raw), &blocks); err != nil {
			out = append(out, m.WithoutOptionalContent(blocksKey))
			continue
		}
		kept := make([]json.RawMessage, 0, len(blocks))
		for _, block := range blocks {
			var head struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(block, &head) != nil {
				continue
			}
			if head.Type == "text" || head.Type == "tool_use" {
				kept = append(kept, block)
			}
		}
		if len(kept) == 0 {
			out = append(out, m.WithoutOptionalContent(blocksKey))
			continue
		}
		sanitized, err := json.Marshal(kept)
		if err != nil {
			out = append(out, m.WithoutOptionalContent(blocksKey))
			continue
		}
		out = append(out, m.WithOptionalContent(blocksKey, string(sanitized)))
	}
	return out
}
