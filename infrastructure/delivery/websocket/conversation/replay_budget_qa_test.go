package wsconversation

// QA round 2 — the replay bound is a property of the socket queue, not of the
// TCP buffers between the server and the harness.
//
// The black-box scenario (`test/repro`: tr) can only prove survival under load
// on loopback, where several MiB of kernel buffering hide a full queue. These
// tests sit inside the hub instead: the journal is filled to an exact count,
// the watch is answered against that count, and one live frame is emitted
// *before the test reads anything* — so the only place those frames can wait is
// `conn.out`, the queue the bound is about.
//
// What is proven: the boundary the server applies is
// `replayBudget()-replayHeadroom` — a window of exactly that size replays and
// stays live, one frame more answers the snapshot, and in both cases the
// socket object is not killed by the live frame that follows. What is not
// proven: that a pre-headroom server would have died, which needs the kernel to
// stop absorbing frames.

import (
	"context"
	"fmt"
	"testing"
	"time"

	appconversation "github.com/jahrulnr/nusallm/application/conversation"
	"github.com/jahrulnr/nusallm/infrastructure/domain/event"
)

// waitForOneSocket waits until the hub holds exactly one attached socket — the
// watcher, after the owner is closed — and returns its server-side conn so the
// test can read the queue's own liveness flag.
func waitForOneSocket(t *testing.T, h *Hub) *conn {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.mu.Lock()
		var only *conn
		n := 0
		for c := range h.sockets {
			only = c
			n++
		}
		h.mu.Unlock()
		if n == 1 {
			return only
		}
		if time.Now().After(deadline) {
			t.Fatalf("hub holds %d sockets, want exactly the watcher", n)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// waitForSubscribed waits until the hub has attached exactly one socket to the
// turn — the watcher's answer has been written and the attach recorded — so a
// live frame emitted afterwards is guaranteed to arrive after it, never into
// the journal first.
func waitForSubscribed(t *testing.T, h *Hub, turnID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.mu.Lock()
		n := 0
		if tr, ok := h.turns[turnID]; ok {
			n = len(tr.subscribed)
		}
		h.mu.Unlock()
		if n == 1 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("turn %s has %d subscribers, want the watcher", turnID, n)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func startJournal(t *testing.T, h *Hub, turn *emitTurn, convID, turnID string, frames int) {
	t.Helper()
	owner := dial(t, h)
	sendCommand(t, owner, `{"type":"turn.send","requestId":"s1","payload":{"content":"x"}}`)
	opened := readFrame(t, owner)
	if opened.ConversationID != convID || opened.TurnID != turnID {
		t.Fatalf("opened = %+v, want %s/%s", opened, convID, turnID)
	}
	<-turn.started
	// Park the owner so the journal grows with no subscriber queue to kill.
	owner.Close()
	for i := range frames - 1 { // opened is seq 0
		turn.emit <- event.NewDeltaEvent(fmt.Sprintf("d%d", i))
	}
	waitForJournal(t, h, turnID, frames)
}

// TestQAReplayBudgetBoundaryStaysLive fills the journal to exactly the replay
// budget, watches from -1, and emits the next live frame before reading
// anything: every replayed frame plus that delta has to fit the socket queue.
func TestQAReplayBudgetBoundaryStaysLive(t *testing.T) {
	turn := newEmitTurn()
	h := newHub(t, stubSender{fn: turn.send})
	owner := dial(t, h)
	sendCommand(t, owner, `{"type":"turn.send","requestId":"s1","payload":{"content":"x"}}`)
	opened := readFrame(t, owner)
	convID, turnID := opened.ConversationID, opened.TurnID
	<-turn.started
	owner.Close()

	budget := outboundQueueCap - replayHeadroom
	for i := range budget - 1 {
		turn.emit <- event.NewDeltaEvent(fmt.Sprintf("d%d", i))
	}
	waitForJournal(t, h, turnID, budget)

	watcher := dial(t, h)
	serverConn := waitForOneSocket(t, h)
	sendCommand(t, watcher, fmt.Sprintf(
		`{"type":"turn.watch","requestId":"w1","conversationId":%q,"turnId":%q,"afterSeq":-1}`,
		convID, turnID))
	waitForSubscribed(t, h, turnID)

	// The live frame is enqueued before the client reads the replay: this is
	// the moment the queue must still have room for it.
	turn.emit <- event.NewDeltaEvent("live-at-budget")
	waitForJournal(t, h, turnID, budget+1)
	if serverConn.closed.Load() {
		t.Fatal("socket was killed by the live frame at the replay-budget boundary")
	}

	// A window of exactly the budget replays verbatim — no snapshot.
	for seq := range budget {
		f := readFrame(t, watcher)
		if f.Seq == nil || *f.Seq != seq {
			t.Fatalf("replayed frame %d = %+v, want seq %d (got a resync?)", seq, f, seq)
		}
	}
	if f := readFrame(t, watcher); f.Type != "turn.delta" || f.Seq == nil || *f.Seq != budget {
		t.Fatalf("live frame at the budget boundary = %+v, want seq %d", f, budget)
	}
	close(turn.release)
	if f := readFrame(t, watcher); f.Type != "turn.completed" {
		t.Fatalf("terminal = %+v", f)
	}
}

// TestQAReplayOverBudgetResyncsAndStaysLive asks for one frame more than the
// budget: the server must answer the snapshot (leaving the queue room for live
// frames) and keep the socket attached.
func TestQAReplayOverBudgetResyncsAndStaysLive(t *testing.T) {
	turn := newEmitTurn()
	h := newHub(t, stubSender{fn: turn.send})
	owner := dial(t, h)
	sendCommand(t, owner, `{"type":"turn.send","requestId":"s1","payload":{"content":"x"}}`)
	opened := readFrame(t, owner)
	convID, turnID := opened.ConversationID, opened.TurnID
	<-turn.started
	owner.Close()

	budget := outboundQueueCap - replayHeadroom
	for i := range budget { // opened + budget deltas = one more than the budget
		turn.emit <- event.NewDeltaEvent(fmt.Sprintf("d%d", i))
	}
	waitForJournal(t, h, turnID, budget+1)

	watcher := dial(t, h)
	serverConn := waitForOneSocket(t, h)
	sendCommand(t, watcher, fmt.Sprintf(
		`{"type":"turn.watch","requestId":"w1","conversationId":%q,"turnId":%q,"afterSeq":-1}`,
		convID, turnID))
	waitForSubscribed(t, h, turnID)

	turn.emit <- event.NewDeltaEvent("live-over-budget")
	waitForJournal(t, h, turnID, budget+2)
	if serverConn.closed.Load() {
		t.Fatal("socket was killed after an over-budget watch")
	}

	snapshot := readFrame(t, watcher)
	if snapshot.Type != "turn.resync_required" {
		t.Fatalf("over-budget watch = %+v, want the snapshot", snapshot)
	}
	if f := readFrame(t, watcher); f.Type != "turn.delta" || f.Seq == nil || *f.Seq != budget+1 {
		t.Fatalf("live frame after the snapshot = %+v, want seq %d", f, budget+1)
	}
	close(turn.release)
	if f := readFrame(t, watcher); f.Type != "turn.completed" {
		t.Fatalf("terminal after the snapshot = %+v", f)
	}
}

var _ = context.Background
var _ = appconversation.SendResult{}
