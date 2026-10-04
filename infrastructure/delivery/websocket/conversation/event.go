package wsconversation

import (
	dtransport "github.com/jahrulnr/nusallm/infrastructure/delivery/transport"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/event"
)

// onEvent projects one application turn event onto the turn's
// subscriber set under h.mu — the lock keeps a turn's frames totally
// ordered and blocks post-terminal emission. Content-bearing events
// (delta, tool input, tool output, reasoning) are chunked for outbound
// bounding: every nonempty provider chunk crosses in order, split at
// the byte bound. Reasoning also gets one phase marker per stretch.
// Mid-turn errors are the sender's retry noise — the job's own return
// produces the single terminal frame.
func (h *Hub) onEvent(t *turn, ev event.TurnEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if t.terminal || t.overflowed {
		return
	}
	switch ev.Kind() {
	case enum.TurnEventDelta:
		t.inReasoning = false
		for _, chunk := range splitDelta(ev.Delta(), outboundChunkBytes) {
			h.emitLocked(t, "turn.delta", deltaPayload{Text: chunk}, false)
		}
	case enum.TurnEventReasoning, enum.TurnEventReasoningSummary:
		// Both raw reasoning and provider-supplied summaries remain in
		// arrival order on the owner-facing transcript.
		if !t.inReasoning {
			t.inReasoning = true
			if !h.emitLocked(t, "turn.phase", phasePayload{Phase: "reasoning"}, false) {
				h.markOverflowedLocked(t)
			}
		}
		for _, chunk := range splitDelta(ev.Delta(), outboundChunkBytes) {
			if !h.emitLocked(t, "turn.reasoning", reasoningPayload{Text: chunk}, false) {
				h.markOverflowedLocked(t)
				break
			}
		}
	case enum.TurnEventToolCall:
		t.inReasoning = false
		tc := ev.ToolCall()
		if !h.emitLocked(t, "turn.tool_call", toolCallPayload{
			CallID: tc.ID(),
			Name:   tc.Name(),
		}, false) {
			// Provider-originated metadata exceeded the frame bound —
			// terminate this turn only, sanitized.
			h.markOverflowedLocked(t)
			break
		}
		// The exact argument string is streamed separately so a large
		// input can never turn the call metadata into an oversized frame.
		for _, chunk := range splitDelta(tc.Input(), outboundChunkBytes) {
			if !h.emitLocked(t, "turn.tool_input", toolInputPayload{
				CallID: tc.ID(),
				Text:   chunk,
			}, false) {
				h.markOverflowedLocked(t)
				break
			}
		}
	case enum.TurnEventToolOutput:
		// Live stream content for the local owner — every nonempty
		// chunk crosses in order at the outbound byte bound. It is
		// progress, not proof the call finished.
		t.inReasoning = false
		callID := ev.ToolCall().ID()
		for _, chunk := range splitDelta(ev.Delta(), outboundChunkBytes) {
			if !h.emitLocked(t, "turn.tool_output", toolOutputPayload{
				CallID: callID,
				Text:   chunk,
			}, false) {
				h.markOverflowedLocked(t)
				break
			}
		}
	case enum.TurnEventSteer:
		// A folded steering line is user input landing mid-turn —
		// subscribers see it in position rather than on a history
		// re-read after the terminal.
		t.inReasoning = false
		for _, chunk := range splitDelta(ev.Delta(), outboundChunkBytes) {
			if !h.emitLocked(t, "turn.steer", steerPayload{Text: chunk}, false) {
				h.markOverflowedLocked(t)
				break
			}
		}
	case enum.TurnEventFinished:
		t.inReasoning = false
		if !h.emitLocked(t, "turn.step_finished", stepFinishedPayload{
			FinishReason: ev.FinishReason().String(),
			Usage:        dtransport.MarshalUsage(ev.Usage()),
		}, false) {
			h.markOverflowedLocked(t)
		}
	case enum.TurnEventError:
		// Intermediate provider errors never cross; the terminal frame
		// is the send's single reported outcome.
	}
}

// markOverflowedLocked flags a turn whose own frames can't be
// serialized inside the bound — provider-controlled strings (call ids,
// tool names) can exceed it. The context cancels so the job surfaces
// its real receipt; runTurn then emits the sanitized 413 terminal.
// No further stream events emit while overflowed. Callers hold h.mu.
func (h *Hub) markOverflowedLocked(t *turn) {
	if t.overflowed {
		return
	}
	t.overflowed = true
	t.turnCancel()
}

// classifyError maps a send failure through the shared delivery
// classification — upstream 429s report upstream_error, a full
// conversation mailbox reports queue_full, causes never cross.
func classifyError(err error) (int, string, string) {
	return dtransport.ClassifyError(err)
}
