package wsconversation

import (
	"strings"

	appjob "github.com/jahrulnr/nusallm/application/job"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

// steerRecord is one bounded submitted-input record a send turn keeps
// for ack dedup, fold correlation, and terminal snapshots. The receipt's
// state is the single source of truth; projected flips once the fold's
// turn.steer chunk crossed, so one persisted input renders once.
type steerRecord struct {
	inputID   string // the turn.steer command's requestId
	content   string
	receipt   *appjob.SteeringReceipt
	projected bool
}

// turnOutcome records the emitted terminal verdict so a late
// turn.resync_required can replay the stored outcome instead of a bare
// frame.
type turnOutcome struct {
	typ     string // "completed" or "failed"
	payload terminalPayload
}

// steeringSnapshotLocked reads each record's atomic receipt state in
// submission order — the bounded snapshot terminal and resync frames
// carry. Callers hold h.mu; receipts transition under the mailbox lock
// or the turn loop, never into the hub.
func (t *turn) steeringSnapshotLocked() []steerStatePayload {
	if len(t.steerRecords) == 0 {
		return nil
	}
	out := make([]steerStatePayload, 0, len(t.steerRecords))
	for _, rec := range t.steerRecords {
		out = append(out, steerStatePayload{
			InputID: rec.inputID,
			State:   string(rec.receipt.State()),
		})
	}
	return out
}

// resyncForLocked builds the resync_required body for a found turn:
// "active" while it runs, the stored terminal verdict once finished —
// the retained outcome replays instead of a bare frame. Callers hold
// h.mu.
func (h *Hub) resyncForLocked(t *turn) resyncPayload {
	p := resyncPayload{Steering: t.steeringSnapshotLocked()}
	if t.terminal && t.outcome != nil {
		p.Turn = t.outcome.typ
		p.Terminal = &t.outcome.payload
	} else {
		p.Turn = "active"
	}
	return p
}

// turnSteer submits one input into an exact send turn's mailbox. The
// command's requestId becomes the input's inputId — deduped per turn —
// and the ack is the journaled turn.steer_ack: accepted is never
// consumed; the receipt's state decides that when the fold lands.
func (h *Hub) turnSteer(c *conn, cmd command) {
	if cmd.AfterSeq != nil {
		c.push(errorFrame(cmd.RequestID, "invalid_argument", "turn.steer accepts no afterSeq"))
		return
	}
	if len(cmd.RequestID) > maxControlRequestIDBytes {
		c.push(errorFrame(cmd.RequestID, "invalid_argument", "turn.steer requestId exceeds the control bound"))
		return
	}
	convID, ok := commandConvID(c, cmd)
	if !ok {
		return
	}
	if valueobject.ValidID(cmd.TurnID) != nil {
		c.push(errorFrame(cmd.RequestID, "invalid_argument", "turn.steer requires a valid turnId"))
		return
	}
	var payload steerCommandPayload
	if cmd.Payload == nil || !decodeStrict(cmd.Payload, &payload) {
		c.push(errorFrame(cmd.RequestID, "invalid_argument", "turn.steer payload is invalid"))
		return
	}
	if strings.TrimSpace(payload.Content) == "" {
		c.push(errorFrame(cmd.RequestID, "invalid_argument", "turn.steer requires non-empty content"))
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	t, found := h.turns[cmd.TurnID]
	if !found || t.terminal || t.conversationID != convID {
		c.push(errorFrame(cmd.RequestID, "not_found", "no live turn with that id"))
		return
	}
	if t.kind != turnSend || t.steering == nil {
		c.push(errorFrame(cmd.RequestID, "invalid_argument", "that turn does not take steering"))
		return
	}
	if prev, dup := t.steerByInput[cmd.RequestID]; dup {
		if prev.content != payload.Content {
			c.push(errorFrame(cmd.RequestID, "invalid_argument", "steering requestId reused with different content"))
			return
		}
		// Idempotent re-ack — same input, same verdict.
		h.emitLocked(t, "turn.steer_ack", steerInputPayload{InputID: prev.inputID}, false)
		return
	}
	if len(t.steerRecords) >= maxSteerInputs {
		c.push(errorFrame(cmd.RequestID, "queue_full", "turn steering submission bound reached"))
		return
	}
	receipt := t.steering.Submit(payload.Content)
	rec := &steerRecord{inputID: cmd.RequestID, content: payload.Content, receipt: receipt}
	t.steerRecords = append(t.steerRecords, rec)
	t.steerByInput[cmd.RequestID] = rec
	h.emitLocked(t, "turn.steer_ack", steerInputPayload{InputID: rec.inputID}, false)
}

// turnSteerRecall withdraws one exact submitted input while it is still
// pending — claimed or persisted input answers not_found and is never
// restored, while a withdrawn input re-emits the journaled
// turn.steer_recalled so a lost ack reconciles idempotently.
func (h *Hub) turnSteerRecall(c *conn, cmd command) {
	if cmd.AfterSeq != nil {
		c.push(errorFrame(cmd.RequestID, "invalid_argument", "turn.steer_recall accepts no afterSeq"))
		return
	}
	if len(cmd.RequestID) > maxControlRequestIDBytes {
		c.push(errorFrame(cmd.RequestID, "invalid_argument", "turn.steer_recall requestId exceeds the control bound"))
		return
	}
	convID, ok := commandConvID(c, cmd)
	if !ok {
		return
	}
	if valueobject.ValidID(cmd.TurnID) != nil {
		c.push(errorFrame(cmd.RequestID, "invalid_argument", "turn.steer_recall requires a valid turnId"))
		return
	}
	var payload steerRecallCommandPayload
	if cmd.Payload == nil || !decodeStrict(cmd.Payload, &payload) || strings.TrimSpace(payload.InputID) == "" {
		c.push(errorFrame(cmd.RequestID, "invalid_argument", "turn.steer_recall requires a payload.inputId"))
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	t, found := h.turns[cmd.TurnID]
	if !found || t.terminal || t.conversationID != convID {
		c.push(errorFrame(cmd.RequestID, "not_found", "no live turn with that id"))
		return
	}
	if t.kind != turnSend || t.steering == nil {
		c.push(errorFrame(cmd.RequestID, "invalid_argument", "that turn does not take steering"))
		return
	}
	rec, found := t.steerByInput[payload.InputID]
	if !found {
		c.push(errorFrame(cmd.RequestID, "not_found", "no steering input with that id"))
		return
	}
	switch {
	case t.steering.Recall(rec.receipt):
		h.emitLocked(t, "turn.steer_recalled", steerInputPayload{InputID: rec.inputID}, false)
	case rec.receipt.State() == appjob.SteeringRecalled:
		// Withdrawn already — dedupe the success idempotently.
		h.emitLocked(t, "turn.steer_recalled", steerInputPayload{InputID: rec.inputID}, false)
	default:
		// Claimed or persisted — consumed input is never restored.
		c.push(errorFrame(cmd.RequestID, "not_found", "steering input already consumed"))
	}
}
