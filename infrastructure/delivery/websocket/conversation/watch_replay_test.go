package wsconversation

// T-04 / T-17 — a watch on a live turn attaches after its snapshot, and a
// journal replay that cannot fit the socket's queue answers the snapshot
// instead of filling the queue and killing the socket.

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	appconversation "github.com/jahrulnr/nusallm/application/conversation"
	"github.com/jahrulnr/nusallm/infrastructure/domain/event"
)

// emitTurn is a sender that parks until the test releases it, forwarding
// whatever the test pushes onto emit. started receives convID once.
type emitTurn struct {
	started chan string
	emit    chan event.TurnEvent
	release chan struct{}
}

func newEmitTurn() *emitTurn {
	return &emitTurn{
		started: make(chan string, 4),
		emit:    make(chan event.TurnEvent, 1024),
		release: make(chan struct{}),
	}
}

func (e *emitTurn) send(ctx context.Context, convID, _, _, _ string,
	onEvent event.TurnEventHandler) (appconversation.SendResult, error) {
	e.started <- convID
	for {
		select {
		case ev := <-e.emit:
			onEvent(ev)
		case <-e.release:
			return appconversation.SendResult{ConversationID: convID, UserMessagePersisted: true}, nil
		case <-ctx.Done():
			return appconversation.SendResult{ConversationID: convID, UserMessagePersisted: true}, nil
		}
	}
}

// A client that claims a seq the turn never emitted still joins the live
// turn: the snapshot is not the end of the relationship. Without the
// attach the tab misses the rest of the stream and its terminal.
func TestWatchAttachesWhenTheClientIsAheadOfTheJournal(t *testing.T) {
	turn := newEmitTurn()
	h := newHub(t, stubSender{fn: turn.send})
	owner := dial(t, h)
	sendCommand(t, owner, `{"type":"turn.send","requestId":"s1","payload":{"content":"x"}}`)
	opened := readFrame(t, owner)
	convID, turnID := opened.ConversationID, opened.TurnID
	<-turn.started

	watcher := dial(t, h)
	sendCommand(t, watcher, fmt.Sprintf(
		`{"type":"turn.watch","requestId":"w1","conversationId":%q,"turnId":%q,"afterSeq":99}`,
		convID, turnID))
	f := readFrame(t, watcher)
	if f.Type != "turn.resync_required" || f.RequestID != "w1" || f.TurnID != turnID {
		t.Fatalf("ahead-of-journal watch = %+v, want the snapshot", f)
	}
	var p resyncPayload
	if err := json.Unmarshal(f.Payload, &p); err != nil || p.Turn != "active" {
		t.Fatalf("snapshot = %+v, %v — want an active turn", p, err)
	}

	// The live delta and the terminal both reach the watcher.
	turn.emit <- event.NewDeltaEvent("after-snapshot")
	if f := readFrame(t, watcher); f.Type != "turn.delta" || f.Seq == nil || *f.Seq != 1 {
		t.Fatalf("live frame after the snapshot = %+v", f)
	}
	close(turn.release)
	if f := readFrame(t, watcher); f.Type != "turn.completed" {
		t.Fatalf("terminal after the snapshot = %+v", f)
	}
}

// A replay that fits the queue stays byte-verbatim: every retained frame
// after afterSeq, in order, with its original seq.
func TestWatchReplaysWhenTheWindowFitsTheQueue(t *testing.T) {
	turn := newEmitTurn()
	h := newHub(t, stubSender{fn: turn.send})
	owner := dial(t, h)
	sendCommand(t, owner, `{"type":"turn.send","requestId":"s1","payload":{"content":"x"}}`)
	opened := readFrame(t, owner)
	convID, turnID := opened.ConversationID, opened.TurnID
	<-turn.started

	deltas := 8
	for i := range deltas {
		turn.emit <- event.NewDeltaEvent(fmt.Sprintf("d%d", i))
	}
	for range deltas {
		readFrame(t, owner)
	}

	watcher := dial(t, h)
	sendCommand(t, watcher, fmt.Sprintf(
		`{"type":"turn.watch","requestId":"w1","conversationId":%q,"turnId":%q,"afterSeq":-1}`,
		convID, turnID))
	// opened + every delta, in order and with the original seq.
	want := []int{0}
	for i := range deltas {
		want = append(want, i+1)
	}
	for i, seq := range want {
		f := readFrame(t, watcher)
		if f.Seq == nil || *f.Seq != seq {
			t.Fatalf("replayed frame %d = %+v, want seq %d", i, f, seq)
		}
	}
	// Still live: the next delta is delivered on the same socket.
	turn.emit <- event.NewDeltaEvent("tail")
	if f := readFrame(t, watcher); f.Type != "turn.delta" || f.Seq == nil || *f.Seq != deltas+1 {
		t.Fatalf("live frame after an in-budget replay = %+v", f)
	}
	close(turn.release)
	if f := readFrame(t, watcher); f.Type != "turn.completed" {
		t.Fatalf("terminal = %+v", f)
	}
}

// A window larger than the socket's queue answers the snapshot instead of
// enqueueing it: filling the queue would kill the socket, and the client
// would reconnect into the same death. The live turn still attaches, so
// the terminal arrives.
func TestWatchOversizedWindowResyncsWithoutKillingTheSocket(t *testing.T) {
	turn := newEmitTurn()
	h := newHub(t, stubSender{fn: turn.send})
	owner := dial(t, h)
	sendCommand(t, owner, `{"type":"turn.send","requestId":"s1","payload":{"content":"x"}}`)
	opened := readFrame(t, owner)
	convID, turnID := opened.ConversationID, opened.TurnID
	<-turn.started
	// The owner socket cannot take a journal-sized burst; close it so its
	// bounded queue never kills anything, and let the journal grow.
	owner.Close()

	flood := outboundQueueCap + 40
	for i := range flood {
		turn.emit <- event.NewDeltaEvent(fmt.Sprintf("d%d", i))
	}
	waitForJournal(t, h, turnID, flood+1)

	watcher := dial(t, h)
	sendCommand(t, watcher, fmt.Sprintf(
		`{"type":"turn.watch","requestId":"w1","conversationId":%q,"turnId":%q,"afterSeq":-1}`,
		convID, turnID))
	f := readFrame(t, watcher)
	if f.Type != "turn.resync_required" || f.RequestID != "w1" || f.TurnID != turnID {
		t.Fatalf("oversized watch = %+v, want the snapshot", f)
	}
	var p resyncPayload
	if err := json.Unmarshal(f.Payload, &p); err != nil || p.Turn != "active" {
		t.Fatalf("snapshot = %+v, %v — want an active turn", p, err)
	}

	// The socket survived the answer: it is attached and reaches the
	// terminal.
	close(turn.release)
	if f := readFrame(t, watcher); f.Type != "turn.completed" {
		t.Fatalf("terminal after an oversized replay = %+v", f)
	}
}

// waitForJournal blocks until the turn's journal has accepted want frames
// (seq is the next seq to mint), so a watch observes a stable window.
func waitForJournal(t *testing.T, h *Hub, turnID string, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.mu.Lock()
		seq := h.turns[turnID].seq
		h.mu.Unlock()
		if seq >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("journal reached seq %d, want %d", seq, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A window that would exactly fill the queue is answered with the snapshot,
// not replayed: the replay would leave no slot for the live frames that
// follow, so the next delta would kill the socket — the reconnect loop the
// bound exists to prevent. The live turn attaches, a live delta arrives,
// and the terminal lands on the same socket.
func TestWatchReplayLeavesHeadroomForLiveFrames(t *testing.T) {
	turn := newEmitTurn()
	h := newHub(t, stubSender{fn: turn.send})
	owner := dial(t, h)
	sendCommand(t, owner, `{"type":"turn.send","requestId":"s1","payload":{"content":"x"}}`)
	opened := readFrame(t, owner)
	convID, turnID := opened.ConversationID, opened.TurnID
	<-turn.started
	// Park the owner: the journal must grow without a subscriber queue.
	owner.Close()

	// opened + (cap-1) deltas = exactly outboundQueueCap journaled frames.
	for i := range outboundQueueCap - 1 {
		turn.emit <- event.NewDeltaEvent(fmt.Sprintf("d%d", i))
	}
	waitForJournal(t, h, turnID, outboundQueueCap)

	watcher := dial(t, h)
	sendCommand(t, watcher, fmt.Sprintf(
		`{"type":"turn.watch","requestId":"w1","conversationId":%q,"turnId":%q,"afterSeq":-1}`,
		convID, turnID))
	f := readFrame(t, watcher)
	if f.Type != "turn.resync_required" {
		t.Fatalf("exact-fit watch = %+v, want the snapshot", f)
	}

	// The live delta right after the answer must not kill the socket.
	turn.emit <- event.NewDeltaEvent("live-after-replay")
	if f := readFrame(t, watcher); f.Type != "turn.delta" {
		t.Fatalf("live frame after the snapshot = %+v", f)
	}
	close(turn.release)
	if f := readFrame(t, watcher); f.Type != "turn.completed" {
		t.Fatalf("terminal after a full journal = %+v", f)
	}
}

// A window that exactly fits the replay budget (queue minus the reserve)
// still replays verbatim, and the live frames that follow keep flowing.
func TestWatchReplayAtTheBudgetBoundaryStaysLive(t *testing.T) {
	turn := newEmitTurn()
	h := newHub(t, stubSender{fn: turn.send})
	owner := dial(t, h)
	sendCommand(t, owner, `{"type":"turn.send","requestId":"s1","payload":{"content":"x"}}`)
	opened := readFrame(t, owner)
	convID, turnID := opened.ConversationID, opened.TurnID
	<-turn.started
	owner.Close()

	frames := outboundQueueCap - replayHeadroom
	for i := range frames - 1 {
		turn.emit <- event.NewDeltaEvent(fmt.Sprintf("d%d", i))
	}
	waitForJournal(t, h, turnID, frames)

	watcher := dial(t, h)
	sendCommand(t, watcher, fmt.Sprintf(
		`{"type":"turn.watch","requestId":"w1","conversationId":%q,"turnId":%q,"afterSeq":-1}`,
		convID, turnID))
	for i := range frames {
		f := readFrame(t, watcher)
		if f.Seq == nil || *f.Seq != i {
			t.Fatalf("replayed frame %d = %+v, want seq %d", i, f, i)
		}
	}
	turn.emit <- event.NewDeltaEvent("live-at-boundary")
	if f := readFrame(t, watcher); f.Type != "turn.delta" || f.Seq == nil || *f.Seq != frames {
		t.Fatalf("live frame at the budget boundary = %+v", f)
	}
	close(turn.release)
	if f := readFrame(t, watcher); f.Type != "turn.completed" {
		t.Fatalf("terminal at the budget boundary = %+v", f)
	}
}
