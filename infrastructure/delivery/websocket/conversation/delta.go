package wsconversation

import "unicode/utf8"

// deltaPayload is the turn.delta body — only assistant output text
// crosses. Reasoning crosses as a phase marker only (see event.go);
// tool arguments and output never cross.
type deltaPayload struct {
	Text string `json:"text"`
}

// splitDelta carves text into chunks of at most max bytes without
// cutting a UTF-8 sequence — one streaming provider chunk must never
// become a single giant outbound frame, and bounded subscriber queues
// must never carry unbounded text. Content is preserved across chunks;
// the per-turn seq keeps the ordering.
func splitDelta(text string, max int) []string {
	var chunks []string
	for len(text) > max {
		cut := max
		for !utf8.ValidString(text[:cut]) && cut > 0 {
			cut--
		}
		if cut == 0 {
			// Pathological lead byte — split on the rune boundary anyway.
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
