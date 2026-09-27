package wsconversation

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	appconversation "github.com/jahrulnr/nusallm/application/conversation"
	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/event"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

// Turn admission, socket, and journal bounds — deployment constants,
// not contract. The hub admits at most maxActiveTurns running turns and
// maxSockets live sockets; a turn's journal is the bounded frame ring
// `turn.watch` replays, retained briefly after terminal.
const (
	maxActiveTurns     = 32
	maxRetainedTurns   = 32 // terminal journals kept alongside actives
	maxSockets         = 64
	journalMaxFrames   = 512
	journalMaxBytes    = 4 << 20 // 4 MiB
	journalMaxAge      = 2 * time.Minute
	outboundChunkBytes = 32 << 10 // max bytes of text per turn.delta frame
	maxFrameBytes      = 1 << 20  // hard cap on any single outbound frame
	shutdownTurnGrace  = 10 * time.Second
	shutdownDrainGrace = 5 * time.Second
)

// Sender is the conversation send/edit use case — the same application
// path the CLI drives. Send's receipt carries the only fact the
// terminal frame reports: whether the user line actually committed.
// Edit's bare error is enough — an edit terminal never claims
// persistence.
type Sender interface {
	Send(ctx context.Context, convID, route, modelID string,
		message string, attachments []valueobject.Attachment,
		onEvent event.TurnEventHandler) (appconversation.SendResult, error)
	Edit(ctx context.Context, convID, msgID, content string,
		onEvent event.TurnEventHandler) error
}

// journaledFrame is one sequenced frame retained for turn.watch replay.
type journaledFrame struct {
	seq int
	raw []byte
}

// turnKind distinguishes the command that opened a turn — terminal
// framing differs (an edit never claims userMessagePersisted).
type turnKind int

const (
	turnSend turnKind = iota
	turnEdit
)

// turn is one admitted command: its bounded frame journal, the live
// subscriber set, and the job's own server-scoped context — disconnected
// sockets never cancel it.
type turn struct {
	id             string
	kind           turnKind
	conversationID string
	requestID      string
	turnCtx        context.Context
	turnCancel     context.CancelFunc
	// job arguments — convID already on the turn.
	route, modelID, msgID, content string

	seq          int
	frames       []journaledFrame
	journalStart int // seq of frames[0]; == seq when the journal is empty
	bytes        int
	terminal     bool
	closedAt     time.Time
	subscribed   map[*conn]struct{}

	// marker coalescing — reasoning stretches and tool-output bursts
	// emit one frame each, not one per provider chunk.
	inReasoning bool
	seenToolOut map[string]struct{}
	// overflowed marks a turn whose own stream frames exceeded the
	// outbound bound — its context is canceled, further events are
	// suppressed, and runTurn emits the sanitized 413 terminal carrying
	// the job's real persistence receipt.
	overflowed bool
}

// Hub owns the process-scoped conversation protocol: every admitted
// turn, its bounded journal, and every live socket. Turns live under
// the hub's own context — a dropped socket changes the subscriber set,
// never the send. Shutdown is the only owner of bulk turn cancellation.
type Hub struct {
	ctx    context.Context
	cancel context.CancelFunc
	sender Sender

	mu         sync.Mutex
	turns      map[string]*turn
	sockets    map[*conn]struct{}
	wg         sync.WaitGroup
	shutting   bool
	turnGrace  time.Duration // bounded wait for turn terminals at shutdown
	drainGrace time.Duration // bounded wait for socket writers to flush
}

// NewHub binds a sender under the given server-lifecycle context —
// callers pass one that survives a signal so Shutdown, not the signal,
// owns terminal emission. A turn's own ctx derives from the hub's.
func NewHub(ctx context.Context, sender Sender) *Hub {
	hctx, cancel := context.WithCancel(ctx)
	return &Hub{
		ctx:        hctx,
		cancel:     cancel,
		sender:     sender,
		turns:      make(map[string]*turn),
		sockets:    make(map[*conn]struct{}),
		turnGrace:  shutdownTurnGrace,
		drainGrace: shutdownDrainGrace,
	}
}

// SocketFull reports whether the socket bound is reached — the upgrade
// edge checks it before upgrading so a full hub answers 503 problem+json
// instead of a half-opened socket. Attach rechecks under the lock.
func (h *Hub) SocketFull() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.shutting || len(h.sockets) >= maxSockets
}

// Attach registers an already-upgraded socket and drives it until
// disconnect. Called by the upgrade boundary on the gin handler
// goroutine.
func (h *Hub) Attach(ws *websocket.Conn) {
	c := newConn(h, ws)
	h.mu.Lock()
	if h.shutting || len(h.sockets) >= maxSockets {
		h.mu.Unlock()
		_ = ws.SetWriteDeadline(time.Now().Add(writeWait))
		_ = ws.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseServiceRestart, "server shutting down or full"))
		_ = ws.Close()
		return
	}
	h.sockets[c] = struct{}{}
	h.mu.Unlock()
	c.serve()
}

// handleCommand validates one inbound frame end-to-end and answers it
// or turns it into work. Validation failure answers an `error` frame on
// the same socket — the socket stays open.
func (h *Hub) handleCommand(c *conn, raw []byte) {
	cmd, ok := decodeCommand(raw)
	if !ok {
		// requestId echoes when it could be parsed — a strict-decode
		// failure still tries the lenient salvage for correlation.
		var salvage struct {
			RequestID string `json:"requestId"`
		}
		_ = json.Unmarshal(raw, &salvage)
		c.push(errorFrame(salvage.RequestID, "invalid_argument", "request is not a valid JSON command"))
		return
	}
	// requestId is mandatory on every command — it correlates the
	// reply/terminal frame for the issuing tab. A blank id isn't an id
	// at all — the reply doesn't echo it.
	if strings.TrimSpace(cmd.RequestID) == "" {
		c.push(errorFrame("", "invalid_argument", "requestId is required"))
		return
	}
	switch cmd.Type {
	case "turn.send":
		h.turnSend(c, cmd)
	case "turn.edit":
		h.turnEdit(c, cmd)
	case "turn.cancel":
		h.turnCancelCommand(c, cmd)
	case "turn.watch":
		h.turnWatch(c, cmd)
	default:
		c.push(errorFrame(cmd.RequestID, "invalid_argument", "unknown command type "+cmd.Type))
	}
}

// commandConvID validates a mandatory envelope conversationId — cancel,
// watch, and edit all name an existing room, so absent or invalid is a
// syntactic rejection.
func commandConvID(c *conn, cmd command) (string, bool) {
	if cmd.ConversationID == nil {
		c.push(errorFrame(cmd.RequestID, "invalid_argument", "conversationId is required"))
		return "", false
	}
	convID := strings.TrimSpace(*cmd.ConversationID)
	if valueobject.ValidID(convID) != nil {
		c.push(errorFrame(cmd.RequestID, "invalid_argument", "conversationId is not a valid id"))
		return "", false
	}
	return convID, true
}

// turnSend validates the send body and admits a bounded turn.
func (h *Hub) turnSend(c *conn, cmd command) {
	// turn.send accepts no turn-scoping envelope fields.
	if cmd.TurnID != "" || cmd.AfterSeq != nil {
		c.push(errorFrame(cmd.RequestID, "invalid_argument", "turn.send accepts no turnId/afterSeq"))
		return
	}
	var payload sendPayload
	if cmd.Payload == nil || !decodeStrict(cmd.Payload, &payload) {
		c.push(errorFrame(cmd.RequestID, "invalid_argument", "turn.send payload is invalid"))
		return
	}
	if strings.TrimSpace(payload.Content) == "" {
		c.push(errorFrame(cmd.RequestID, "invalid_argument", "turn.send requires nonempty payload.content"))
		return
	}
	convID := ""
	if cmd.ConversationID != nil {
		convID = strings.TrimSpace(*cmd.ConversationID)
		if valueobject.ValidID(convID) != nil {
			c.push(errorFrame(cmd.RequestID, "invalid_argument", "conversationId is not a valid id"))
			return
		}
	}
	h.admitAndRun(c, cmd.RequestID, convID, turn{
		kind: turnSend, route: payload.Route, modelID: payload.ModelID, content: payload.Content,
	})
}

// turnEdit validates the edit body and admits a bounded turn. The
// conversation must be named — an edit never mints a room.
func (h *Hub) turnEdit(c *conn, cmd command) {
	if cmd.TurnID != "" || cmd.AfterSeq != nil {
		c.push(errorFrame(cmd.RequestID, "invalid_argument", "turn.edit accepts no turnId/afterSeq"))
		return
	}
	convID, ok := commandConvID(c, cmd)
	if !ok {
		return
	}
	var payload editPayload
	if cmd.Payload == nil || !decodeStrict(cmd.Payload, &payload) {
		c.push(errorFrame(cmd.RequestID, "invalid_argument", "turn.edit payload is invalid"))
		return
	}
	if valueobject.ValidID(payload.MessageID) != nil {
		c.push(errorFrame(cmd.RequestID, "invalid_argument", "turn.edit requires a valid payload.messageId"))
		return
	}
	if strings.TrimSpace(payload.Content) == "" {
		c.push(errorFrame(cmd.RequestID, "invalid_argument", "turn.edit requires nonempty payload.content"))
		return
	}
	h.admitAndRun(c, cmd.RequestID, convID, turn{
		kind: turnEdit, msgID: payload.MessageID, content: payload.Content,
	})
}

// admitAndRun claims a turn slot under the hub lock — admission
// rejection, journal creation, seq-0 `opened`, and the worker's
// accounting all happen before the job starts.
func (h *Hub) admitAndRun(c *conn, requestID, convID string, job turn) {
	h.mu.Lock()
	h.evictJournalsLocked(time.Now())
	active := 0
	for _, t := range h.turns {
		if !t.terminal {
			active++
		}
	}
	if h.shutting {
		h.mu.Unlock()
		c.push(errorFrame(requestID, "unavailable", "server is shutting down"))
		return
	}
	if active >= maxActiveTurns {
		h.mu.Unlock()
		c.push(errorFrame(requestID, "queue_full", "too many active turns"))
		return
	}
	if convID == "" {
		// Fresh conversation minted server-side at admit time.
		conv, err := aggregate.NewConversation("")
		if err != nil {
			h.mu.Unlock()
			c.push(errorFrame(requestID, "internal", "could not mint conversation id"))
			return
		}
		convID = conv.ID()
	}
	tid, err := valueobject.NewID("t", "")
	if err != nil {
		h.mu.Unlock()
		c.push(errorFrame(requestID, "internal", "could not mint turn id"))
		return
	}
	t := &job
	t.id = tid.String()
	t.conversationID = convID
	t.requestID = requestID
	t.subscribed = map[*conn]struct{}{c: {}}
	t.seenToolOut = make(map[string]struct{})
	t.turnCtx, t.turnCancel = context.WithCancel(h.ctx)
	h.turns[t.id] = t
	// Accounted under the lock — Shutdown's Wait can't observe the
	// worker before its Add. `opened` is journaled while still locked
	// so seq 0 provably precedes every stream frame.
	h.wg.Add(1)
	h.emitLocked(t, "turn.opened", nil, true)
	h.mu.Unlock()

	go h.runTurn(t)
}

// runTurn is the turn worker: the job runs on the hub-scoped ctx,
// stream events project onto journal+subscribers, and exactly one
// terminal frame lands — success, classified failure, or shutdown.
func (h *Hub) runTurn(t *turn) {
	defer h.wg.Done()
	onEvent := func(ev event.TurnEvent) { h.onEvent(t, ev) }
	var (
		persisted bool
		err       error
	)
	switch t.kind {
	case turnSend:
		var receipt appconversation.SendResult
		receipt, err = h.sender.Send(t.turnCtx, t.conversationID, t.route, t.modelID, t.content, nil, onEvent)
		persisted = receipt.UserMessagePersisted
	case turnEdit:
		err = h.sender.Edit(t.turnCtx, t.conversationID, t.msgID, t.content, onEvent)
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	defer h.evictJournalsLocked(time.Now())
	if t.terminal {
		return // already terminated
	}
	switch {
	case t.overflowed:
		// A stream frame exceeded the outbound bound — the turn dies
		// with a sanitized 413 and the job's real receipt, never a
		// fabricated persistence claim.
		p := terminalPayload{
			Status:         http.StatusRequestEntityTooLarge,
			Code:           "input_too_large",
			Message:        "stream frame exceeded the outbound bound",
			ResyncRequired: true,
		}
		if t.kind == turnSend {
			p.UserMessagePersisted = &persisted
		}
		h.emitLocked(t, "turn.failed", p, true)
	case err == nil && t.kind == turnSend && !persisted:
		// A send that reports success without a committed user line is
		// an engine bug — never let it look like a persisted success.
		h.emitLocked(t, "turn.failed", terminalPayload{
			Status:               http.StatusInternalServerError,
			Code:                 "internal",
			Message:              "send completed without a persistence receipt",
			UserMessagePersisted: &persisted,
			ResyncRequired:       true,
		}, true)
	case err == nil:
		p := terminalPayload{ResyncRequired: true}
		if t.kind == turnSend {
			p.UserMessagePersisted = &persisted
		}
		h.emitLocked(t, "turn.completed", p, true)
	case h.shutting:
		// Graceful shutdown canceled the turn — classify 503 so the tab
		// knows the turn died with the server. The receipt's real
		// persistence state rides along, never fabricated.
		p := terminalPayload{
			Status:         http.StatusServiceUnavailable,
			Code:           "unavailable",
			Message:        "server shutting down",
			ResyncRequired: true,
		}
		if t.kind == turnSend {
			p.UserMessagePersisted = &persisted
		}
		h.emitLocked(t, "turn.failed", p, true)
	default:
		status, code, message := classifyError(err)
		p := terminalPayload{
			Status:         status,
			Code:           code,
			Message:        message,
			ResyncRequired: true,
		}
		if t.kind == turnSend {
			p.UserMessagePersisted = &persisted
		}
		h.emitLocked(t, "turn.failed", p, true)
	}
	h.finishLocked(t)
}

// turnCancelCommand acks-and-cancels one exact active turn: the ack goes
// out BEFORE the context is canceled so "accepted" is already on the
// wire if cancellation wins the race. An unknown, finished, or
// foreign-conversation turnId is a not_found error, never an ack.
func (h *Hub) turnCancelCommand(c *conn, cmd command) {
	if cmd.AfterSeq != nil || cmd.Payload != nil {
		c.push(errorFrame(cmd.RequestID, "invalid_argument", "turn.cancel accepts no afterSeq/payload"))
		return
	}
	convID, ok := commandConvID(c, cmd)
	if !ok {
		return
	}
	if valueobject.ValidID(cmd.TurnID) != nil {
		c.push(errorFrame(cmd.RequestID, "invalid_argument", "turn.cancel requires a valid turnId"))
		return
	}
	h.mu.Lock()
	t, found := h.turns[cmd.TurnID]
	if !found || t.terminal || t.conversationID != convID {
		h.mu.Unlock()
		c.push(errorFrame(cmd.RequestID, "not_found", "no live turn with that id"))
		return
	}
	c.push(cancelAckFrame(cmd.RequestID, convID, t.id))
	t.turnCancel()
	h.mu.Unlock()
}

// turnWatch attaches a socket to a turn's journal: under one lock it
// replays journaled frames with seq > afterSeq and subscribes for live
// frames, so replay and live never gap or interleave. Unsatisfiable
// watches answer turn.resync_required — missing deltas are never
// fabricated; an active turn with a truncated journal still attaches
// for live frames after the resync notice so the tab sees the terminal.
func (h *Hub) turnWatch(c *conn, cmd command) {
	if cmd.Payload != nil {
		c.push(errorFrame(cmd.RequestID, "invalid_argument", "turn.watch accepts no payload"))
		return
	}
	convID, ok := commandConvID(c, cmd)
	if !ok {
		return
	}
	if valueobject.ValidID(cmd.TurnID) != nil {
		c.push(errorFrame(cmd.RequestID, "invalid_argument", "turn.watch requires a valid turnId"))
		return
	}
	if cmd.AfterSeq == nil || *cmd.AfterSeq < -1 {
		c.push(errorFrame(cmd.RequestID, "invalid_argument", "turn.watch requires afterSeq >= -1"))
		return
	}
	after := *cmd.AfterSeq
	h.mu.Lock()
	// Lookup applies the retention bound too — an expired journal is
	// evicted here, not only at the next admission.
	h.evictJournalsLocked(time.Now())
	t, found := h.turns[cmd.TurnID]
	// Unknown, evicted, foreign-conversation, or newer-than-latest
	// afterSeq: nothing honest to replay.
	if !found || t.conversationID != convID || after > t.seq-1 {
		h.mu.Unlock()
		c.push(resyncFrame(cmd.RequestID, convID, cmd.TurnID))
		return
	}
	// Journal truncation check: the earliest retained seq must cover
	// the requested window start (afterSeq -1 asks for everything).
	if t.journalStart > after+1 {
		// Gap — the client must re-read persisted state regardless.
		c.push(resyncFrame(cmd.RequestID, convID, t.id))
		if t.terminal {
			h.mu.Unlock()
			return // finished turns with gaps get no attach
		}
		// Active: still attach for live frames — the tab sees the
		// terminal while re-reading history over HTTP.
		t.subscribed[c] = struct{}{}
		h.mu.Unlock()
		return
	}
	for _, jf := range t.frames {
		if jf.seq > after {
			c.enqueue(jf.raw)
		}
	}
	if !t.terminal {
		t.subscribed[c] = struct{}{}
	}
	h.mu.Unlock()
}

// emitLocked mints one sequenced frame into the journal and delivers it
// to every subscriber. withRequest attaches requestId — only opened and
// terminal frames carry it (plus the unsequenced command replies).
// Returns false when the frame exceeds the outbound cap — the seq is
// NOT consumed on failure, so the emitted stream stays contiguous.
// Callers hold h.mu.
func (h *Hub) emitLocked(t *turn, typ string, payload any, withRequest bool) bool {
	seq := t.seq
	f := serverFrame{
		Type:           typ,
		ConversationID: t.conversationID,
		TurnID:         t.id,
		Seq:            &seq,
		Payload:        payload,
	}
	if withRequest {
		f.RequestID = t.requestID
	}
	raw, ok := marshalFrame(f)
	if !ok {
		return false
	}
	t.seq++
	// Bounded journal: drop the oldest frames past the frame/byte cap.
	t.frames = append(t.frames, journaledFrame{seq: seq, raw: raw})
	t.bytes += len(raw)
	for len(t.frames) > journalMaxFrames || t.bytes > journalMaxBytes {
		t.bytes -= len(t.frames[0].raw)
		t.frames[0].raw = nil
		t.frames = t.frames[1:]
		t.journalStart = t.frames[0].seq
	}
	for c := range t.subscribed {
		c.enqueue(raw)
	}
	return true
}

// finishLocked seals a turn after its terminal frame: subscribers
// detach, the context releases, and the journal enters retention.
// Callers hold h.mu.
func (h *Hub) finishLocked(t *turn) {
	t.terminal = true
	t.closedAt = time.Now()
	t.subscribed = nil
	t.turnCancel()
}

// dropConn removes a socket from every turn's subscriber set — turns
// run on, their journals intact for watchers.
func (h *Hub) dropConn(c *conn) {
	h.mu.Lock()
	for _, t := range h.turns {
		delete(t.subscribed, c)
	}
	delete(h.sockets, c)
	h.mu.Unlock()
}

// evictJournalsLocked applies the retention bound: terminal turns older
// than journalMaxAge — or in excess of maxRetainedTurns — release their
// journals. Callers hold h.mu.
func (h *Hub) evictJournalsLocked(now time.Time) {
	terminal := 0
	for _, t := range h.turns {
		if t.terminal && now.Sub(t.closedAt) > journalMaxAge {
			delete(h.turns, t.id)
		} else if t.terminal {
			terminal++
		}
	}
	// Over-retained: release oldest terminal journals until the bound
	// holds. Bounded memory beats long retention for a local UI.
	for terminal > maxRetainedTurns {
		var oldest *turn
		for _, t := range h.turns {
			if t.terminal && (oldest == nil || t.closedAt.Before(oldest.closedAt)) {
				oldest = t
			}
		}
		if oldest == nil {
			return
		}
		delete(h.turns, oldest.id)
		terminal--
	}
}

// Shutdown stops admission, cancels every live turn context so senders
// surface quickly, and waits (bounded) for workers to land their real
// terminal frame before sockets drain — the terminal enqueued by a
// worker must precede the drain sentinel, so sockets close only after
// the wait. If the grace lapses, remaining workers close without a
// fabricated terminal — the hub exits regardless.
func (h *Hub) Shutdown() {
	h.mu.Lock()
	if h.shutting {
		h.mu.Unlock()
		return
	}
	h.shutting = true
	for _, t := range h.turns {
		if !t.terminal {
			t.turnCancel()
		}
	}
	h.mu.Unlock()

	done := make(chan struct{})
	go func() {
		h.wg.Wait()
		close(done)
	}()
	timer := time.NewTimer(h.turnGrace)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		// Turns still running outlive the socket layer; the hub context
		// cancellation below unblocks them for process exit.
	}

	// Terminals landed (or grace lapsed) — drain sockets politely, then
	// force-close any writer that couldn't flush in time.
	h.mu.Lock()
	sockets := make([]*conn, 0, len(h.sockets))
	for c := range h.sockets {
		c.shutdown()
		sockets = append(sockets, c)
	}
	h.mu.Unlock()
	drain := time.NewTimer(h.drainGrace)
	defer drain.Stop()
	for _, c := range sockets {
		select {
		case <-c.flushed:
		case <-drain.C:
		}
	}
	h.cancel()
	h.mu.Lock()
	for c := range h.sockets {
		c.kill()
	}
	h.mu.Unlock()
}
