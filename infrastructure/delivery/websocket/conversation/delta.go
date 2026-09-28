package wsconversation

import "unicode/utf8"

// deltaPayload is the turn.delta body — assistant output text only.
// Reasoning, tool input, and tool output cross in their own bounded
// chunk frames for the local owner.
type deltaPayload struct {
	Text string `json:"text"`
}

// splitDelta carves text into chunks of at most max bytes without
// cutting a UTF-8 sequence — one streaming provider chunk must never
// become a single giant outbound frame, and bounded subscriber queues
// must never carry unbounded text. Boundary checks inspect only the
// rune around the cut: arbitrary tool bytes can be malformed, and an
// O(prefix²) validity scan under the hub lock would stall every turn.
// Content is preserved across chunks; the per-turn seq keeps ordering.
func splitDelta(text string, max int) []string {
	var chunks []string
	for len(text) > max {
		cut := max
		if !utf8.RuneStart(text[cut]) {
			start := cut
			for start > 0 && cut-start < utf8.UTFMax && !utf8.RuneStart(text[start]) {
				start--
			}
			if utf8.RuneStart(text[start]) {
				if _, size := utf8.DecodeRuneInString(text[start:]); start+size > cut {
					cut = start
				}
			}
		}
		if cut == 0 {
			// A rune wider than max cannot fit whole; split it anyway.
			_, size := utf8.DecodeRuneInString(text)
			cut = size
		}
		chunks = append(chunks, text[:cut])
		text = text[cut:]
	}
	if len(text) > 0 {
		chunks = append(chunks, text)
	}
	return chunks
}
