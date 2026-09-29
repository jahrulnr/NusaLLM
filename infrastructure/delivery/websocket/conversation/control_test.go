package wsconversation

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"

	appconversation "github.com/jahrulnr/nusallm/application/conversation"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/event"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
)

func TestTurnEditLifecycle(t *testing.T) {
	var gotMsg, gotContent string
	h := newHub(t, stubSender{
		fn: func(context.Context, string, string, string, string, event.TurnEventHandler) (appconversation.SendResult, error) {
			return appconversation.SendResult{UserMessagePersisted: true}, nil
		},
		efn: func(_ context.Context, convID, msgID, content string, onEvent event.TurnEventHandler) error {
			gotMsg, gotContent = msgID, content
			onEvent(event.NewDeltaEvent("regenerated"))
			return nil
		},
	})
	ws := dial(t, h)
	sendCommand(t, ws, `{"type":"turn.edit","requestId":"e1","conversationId":"room-1","payload":{"messageId":"m_abc","content":"rewritten"}}`)

	var term frame
	var types []string
	for {
		f := readFrame(t, ws)
		types = append(types, f.Type)
		if f.Type == "turn.completed" || f.Type == "turn.failed" {
			term = f
			break
		}
	}
	if gotMsg != "m_abc" || gotContent != "rewritten" {
		t.Fatalf("edit args %q %q", gotMsg, gotContent)
	}
	if fmt.Sprint(types) != fmt.Sprint([]string{"turn.opened", "turn.delta", "turn.completed"}) {
		t.Fatalf("frames %v", types)
	}
	if term.ConversationID != "room-1" || term.RequestID != "e1" {
		t.Fatalf("envelope %+v", term)
	}
	// Edit terminals never claim userMessagePersisted.
	var raw map[string]any
	if err := json.Unmarshal(term.Payload, &raw); err != nil {
		t.Fatal(err)
	}
	if _, present := raw["userMessagePersisted"]; present {
		t.Fatalf("edit terminal claims userMessagePersisted: %s", term.Payload)
	}
	if raw["resyncRequired"] != true {
		t.Fatalf("edit terminal payload: %s", term.Payload)
	}
}

func TestTurnEditFailed(t *testing.T) {
	h := newHub(t, stubSender{
		fn: func(context.Context, string, string, string, string, event.TurnEventHandler) (appconversation.SendResult, error) {
			return appconversation.SendResult{UserMessagePersisted: true}, nil
		},
		efn: func(context.Context, string, string, string, event.TurnEventHandler) error {
			return apperror.NewNotFoundError("message m_gone", nil)
		},
	})
	ws := dial(t, h)
	sendCommand(t, ws, `{"type":"turn.edit","requestId":"e1","conversationId":"room-1","payload":{"messageId":"m_gone","content":"x"}}`)
	var f frame
	for f = readFrame(t, ws); f.Type != "turn.failed"; f = readFrame(t, ws) {
	}
	var p map[string]any
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if int(p["status"].(float64)) != 404 || p["code"] != "not_found" {
		t.Fatalf("edit terminal %s", f.Payload)
	}
	if _, present := p["userMessagePersisted"]; present {
		t.Fatal("failed edit must not claim persistence")
	}
}

// blockedTurn is a sender that parks Send until ctx dies or release
// fires — enough to drive cancel/shutdown scenarios deterministically.
type blockedTurn struct {
	release  chan struct{}
	started  chan string // receives convID once Send is running
	onCancel chan struct{}
}

func newBlockedTurn() *blockedTurn {
	return &blockedTurn{release: make(chan struct{}), started: make(chan string, 8), onCancel: make(chan struct{}, 8)}
}

func (b *blockedTurn) send(ctx context.Context, convID, _, _, _ string,
	onEvent event.TurnEventHandler) (appconversation.SendResult, error) {
	b.started <- convID
	select {
	case <-b.release:
		return appconversation.SendResult{ConversationID: convID, UserMessagePersisted: true}, nil
	case <-ctx.Done():
		select {
		case b.onCancel <- struct{}{}:
		default:
		}
		return appconversation.SendResult{ConversationID: convID, UserMessagePersisted: true},
			apperror.New(408, "send canceled", ctx.Err())
	}
}

func TestCancelExactTurn(t *testing.T) {
	b := newBlockedTurn()
	h := newHub(t, stubSender{fn: b.send})
	ws := dial(t, h)

	sendCommand(t, ws, `{"type":"turn.send","requestId":"s1","payload":{"content":"x"}}`)
	f := readFrame(t, ws)
	if f.Type != "turn.opened" {
		t.Fatalf("%+v", f)
	}
	convID, turnID := f.ConversationID, f.TurnID
	<-b.started

	sendCommand(t, ws, fmt.Sprintf(`{"type":"turn.cancel","requestId":"c1","conversationId":%q,"turnId":%q}`, convID, turnID))
	// The ack answers the cancel command before the canceled turn's
	// terminal — ack has no seq, carries the cancel's requestId.
	f = readFrame(t, ws)
	if f.Type != "turn.cancel_ack" || f.RequestID != "c1" || f.TurnID != turnID || f.Seq != nil {
		t.Fatalf("ack %+v", f)
	}
	f = readFrame(t, ws)
	if f.Type != "turn.failed" {
		t.Fatalf("canceled turn terminal %+v", f)
	}
	var p terminalPayload
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.Status != 408 || p.Code != "interrupted" {
		t.Fatalf("canceled terminal %d/%s, want 408/interrupted", p.Status, p.Code)
	}
	if p.UserMessagePersisted == nil || !*p.UserMessagePersisted {
		t.Fatal("canceled send reports its real receipt")
	}
	select {
	case <-b.onCancel:
	default:
		t.Fatal("cancel never reached the turn context")
	}
}

func TestCancelRejects(t *testing.T) {
	b := newBlockedTurn()
	h := newHub(t, stubSender{fn: b.send})
	ws := dial(t, h)

	// Unknown turnId.
	sendCommand(t, ws, `{"type":"turn.cancel","requestId":"c1","conversationId":"room","turnId":"t_nope"}`)
	if f := readFrame(t, ws); f.Type != "error" || f.Code != "not_found" || f.RequestID != "c1" {
		t.Fatalf("unknown cancel %+v", f)
	}

	// Live turn, but foreign conversationId.
	sendCommand(t, ws, `{"type":"turn.send","requestId":"s1","payload":{"content":"x"}}`)
	f := readFrame(t, ws)
	convID, turnID := f.ConversationID, f.TurnID
	<-b.started
	sendCommand(t, ws, fmt.Sprintf(`{"type":"turn.cancel","requestId":"c2","conversationId":"other-room","turnId":%q}`, turnID))
	if f := readFrame(t, ws); f.Type != "error" || f.Code != "not_found" {
		t.Fatalf("foreign cancel %+v", f)
	}

	// Finish the turn, then cancel — finished is not_found too.
	close(b.release)
	for f = readFrame(t, ws); f.Type != "turn.completed"; f = readFrame(t, ws) {
	}
	sendCommand(t, ws, fmt.Sprintf(`{"type":"turn.cancel","requestId":"c3","conversationId":%q,"turnId":%q}`, convID, turnID))
	if f := readFrame(t, ws); f.Type != "error" || f.Code != "not_found" {
		t.Fatalf("finished cancel %+v", f)
	}
}

// An ack only means the cancel was accepted: if the job committed
// before the canceled context was observed, the terminal is still
// turn.completed — never a retroactive failure.
func TestCancelAckThenCompleted(t *testing.T) {
	committed := make(chan struct{})
	h := newHub(t, stubSender{fn: func(ctx context.Context, convID, _, _, _ string,
		_ event.TurnEventHandler) (appconversation.SendResult, error) {
		select {
		case <-committed:
			// Committed despite the canceled ctx — the receipt is real.
			return appconversation.SendResult{ConversationID: convID, UserMessagePersisted: true}, nil
		case <-ctx.Done():
			// Keep running even though ctx died — wait for commit.
			<-committed
			return appconversation.SendResult{ConversationID: convID, UserMessagePersisted: true}, nil
		}
	}})
	ws := dial(t, h)
	sendCommand(t, ws, `{"type":"turn.send","requestId":"s1","payload":{"content":"x"}}`)
	f := readFrame(t, ws)
	convID, turnID := f.ConversationID, f.TurnID
	sendCommand(t, ws, fmt.Sprintf(`{"type":"turn.cancel","requestId":"c1","conversationId":%q,"turnId":%q}`, convID, turnID))
	if f = readFrame(t, ws); f.Type != "turn.cancel_ack" {
		t.Fatalf("want ack, got %+v", f)
	}
	close(committed) // the job commits anyway — terminal must be honest
	f = readFrame(t, ws)
	if f.Type != "turn.completed" {
		t.Fatalf("post-commit terminal %+v — expected turn.completed", f)
	}
}

// A cancel that lands after the send committed still acks nothing — the
// completed terminal was already on its way; only a live turn acks.
func TestCancelAfterTerminalIsNotFound(t *testing.T) {
	h := newHub(t, stubSender{fn: func(_ context.Context, convID, _, _, _ string,
		_ event.TurnEventHandler) (appconversation.SendResult, error) {
		return appconversation.SendResult{ConversationID: convID, UserMessagePersisted: true}, nil
	}})
	ws := dial(t, h)
	sendCommand(t, ws, `{"type":"turn.send","requestId":"s1","payload":{"content":"x"}}`)
	var f frame
	for f = readFrame(t, ws); f.Type != "turn.completed"; f = readFrame(t, ws) {
	}
	sendCommand(t, ws, fmt.Sprintf(`{"type":"turn.cancel","requestId":"c1","conversationId":%q,"turnId":%q}`, f.ConversationID, f.TurnID))
	f = readFrame(t, ws)
	if f.Type != "error" || f.Code != "not_found" {
		t.Fatalf("post-terminal cancel %+v — the ack contract is not_found, not a retroactive cancel", f)
	}
}

func TestWatchReplaysFinishedTurn(t *testing.T) {
	h := newHub(t, stubSender{fn: func(_ context.Context, convID, _, _, _ string,
		onEvent event.TurnEventHandler) (appconversation.SendResult, error) {
		onEvent(event.NewDeltaEvent("part-one"))
		onEvent(event.NewDeltaEvent("part-two"))
		return appconversation.SendResult{ConversationID: convID, UserMessagePersisted: true}, nil
	}})
	ws := dial(t, h)
	sendCommand(t, ws, `{"type":"turn.send","requestId":"s1","payload":{"content":"x"}}`)
	var convID, turnID string
	for {
		f := readFrame(t, ws)
		if f.Type == "turn.completed" {
			convID, turnID = f.ConversationID, f.TurnID
			break
		}
	}

	// A second socket replays the whole journal (afterSeq -1).
	ws2 := dial(t, h)
	sendCommand(t, ws2, fmt.Sprintf(`{"type":"turn.watch","requestId":"w1","conversationId":%q,"turnId":%q,"afterSeq":-1}`, convID, turnID))
	var seqs []int
	var gotDelta strings.Builder
	var last frame
	for i := 0; i < 4; i++ { // opened, delta, delta, completed
		last = readFrame(t, ws2)
		if last.Seq == nil {
			t.Fatalf("replayed frame lost seq: %s", last.Type)
		}
		seqs = append(seqs, *last.Seq)
		if last.Type == "turn.delta" {
			var p deltaPayload
			_ = json.Unmarshal(last.Payload, &p)
			gotDelta.WriteString(p.Text)
		}
	}
	if fmt.Sprint(seqs) != "[0 1 2 3]" {
		t.Fatalf("replayed seqs %v", seqs)
	}
	if last.Type != "turn.completed" || last.RequestID != "s1" {
		t.Fatalf("terminal replay %+v — must carry the original requestId", last)
	}
	if gotDelta.String() != "part-onepart-two" {
		t.Fatalf("replay content %q", gotDelta.String())
	}
}

func TestWatchAttachesLive(t *testing.T) {
	b := newBlockedTurn()
	emit := make(chan event.TurnEvent, 8)
	h := newHub(t, stubSender{fn: func(ctx context.Context, convID, _, _, _ string,
		onEvent event.TurnEventHandler) (appconversation.SendResult, error) {
		b.started <- convID
		for {
			select {
			case ev := <-emit:
				onEvent(ev)
			case <-b.release:
				return appconversation.SendResult{ConversationID: convID, UserMessagePersisted: true}, nil
			case <-ctx.Done():
				return appconversation.SendResult{ConversationID: convID}, apperror.New(408, "canceled", ctx.Err())
			}
		}
	}})
	ws := dial(t, h)
	sendCommand(t, ws, `{"type":"turn.send","requestId":"s1","payload":{"content":"x"}}`)
	f := readFrame(t, ws)
	convID, turnID := f.ConversationID, f.TurnID
	<-b.started
	emit <- event.NewDeltaEvent("before-watch")
	if f := readFrame(t, ws); f.Type != "turn.delta" {
		t.Fatalf("%+v", f)
	}

	// Watcher attaches mid-turn at afterSeq -1: replay sees opened +
	// first delta, then live frames flow to both sockets.
	ws2 := dial(t, h)
	sendCommand(t, ws2, fmt.Sprintf(`{"type":"turn.watch","requestId":"w1","conversationId":%q,"turnId":%q,"afterSeq":-1}`, convID, turnID))
	if f := readFrame(t, ws2); f.Type != "turn.opened" || *f.Seq != 0 {
		t.Fatalf("replay start %+v", f)
	}
	if f := readFrame(t, ws2); f.Type != "turn.delta" || *f.Seq != 1 {
		t.Fatalf("replay delta %+v", f)
	}
	emit <- event.NewDeltaEvent("after-watch")
	for _, socket := range []*websocket.Conn{ws, ws2} {
		f := readFrame(t, socket)
		if f.Type != "turn.delta" || *f.Seq != 2 {
			t.Fatalf("live delta on watcher %+v", f)
		}
		var p deltaPayload
		_ = json.Unmarshal(f.Payload, &p)
		if p.Text != "after-watch" {
			t.Fatalf("live text %q", p.Text)
		}
	}
	// Terminal lands on both.
	close(b.release)
	for _, socket := range []*websocket.Conn{ws, ws2} {
		f := readFrame(t, socket)
		if f.Type != "turn.completed" {
			t.Fatalf("watcher terminal %+v", f)
		}
	}
}

func TestWatchSuffixAndResync(t *testing.T) {
	b := newBlockedTurn()
	h := newHub(t, stubSender{fn: b.send})
	ws := dial(t, h)
	sendCommand(t, ws, `{"type":"turn.send","requestId":"s1","payload":{"content":"x"}}`)
	f := readFrame(t, ws)
	convID, turnID := f.ConversationID, f.TurnID
	<-b.started

	// Unknown turn → resync_required, no seq.
	sendCommand(t, ws, fmt.Sprintf(`{"type":"turn.watch","requestId":"w1","conversationId":%q,"turnId":"t_ghost","afterSeq":-1}`, convID))
	if f = readFrame(t, ws); f.Type != "turn.resync_required" || f.RequestID != "w1" || f.TurnID != "t_ghost" || f.Seq != nil {
		t.Fatalf("unknown watch %+v", f)
	}
	// Foreign conversation → resync_required.
	sendCommand(t, ws, fmt.Sprintf(`{"type":"turn.watch","requestId":"w2","conversationId":"elsewhere","turnId":%q,"afterSeq":-1}`, turnID))
	if f = readFrame(t, ws); f.Type != "turn.resync_required" || f.RequestID != "w2" {
		t.Fatalf("foreign watch %+v", f)
	}
	// afterSeq newer than the journal → resync_required.
	sendCommand(t, ws, fmt.Sprintf(`{"type":"turn.watch","requestId":"w3","conversationId":%q,"turnId":%q,"afterSeq":99}`, convID, turnID))
	if f = readFrame(t, ws); f.Type != "turn.resync_required" {
		t.Fatalf("future watch %+v", f)
	}
	// Exact suffix: afterSeq 0 replays nothing (only opened exists),
	// attaches for live.
	sendCommand(t, ws, fmt.Sprintf(`{"type":"turn.watch","requestId":"w4","conversationId":%q,"turnId":%q,"afterSeq":0}`, convID, turnID))
	close(b.release)
	var sawCompleted bool
	for i := 0; i < 4 && !sawCompleted; i++ {
		f := readFrame(t, ws)
		if f.Type == "turn.completed" && f.TurnID == turnID {
			sawCompleted = true
		}
	}
	if !sawCompleted {
		t.Fatal("watcher never saw the live terminal")
	}
}

func TestWatchGapResyncThenLive(t *testing.T) {
	b := newBlockedTurn()
	emitDone := make(chan struct{})
	flood := make(chan struct{})
	h := newHub(t, stubSender{fn: func(ctx context.Context, convID, _, _, _ string,
		onEvent event.TurnEventHandler) (appconversation.SendResult, error) {
		b.started <- convID
		// Hold the flood until the test has the opened frame in hand —
		// the burst kills the owner socket's bounded queue, so nothing
		// may read that socket afterward. Then overflow the journal so
		// early seqs are truncated.
		<-flood
		for i := 0; i < journalMaxFrames+50; i++ {
			onEvent(event.NewDeltaEvent("x"))
		}
		close(emitDone)
		<-b.release
		return appconversation.SendResult{ConversationID: convID, UserMessagePersisted: true}, nil
	}})
	ws := dial(t, h)
	sendCommand(t, ws, `{"type":"turn.send","requestId":"s1","payload":{"content":"x"}}`)
	// turn.opened is the only frame queued before the flood gate opens —
	// the read is deterministic, and convID/turnID never ride the doomed
	// socket past this point.
	f := readFrame(t, ws)
	if f.Type != "turn.opened" {
		t.Fatalf("%+v", f)
	}
	convID, turnID := f.ConversationID, f.TurnID
	close(flood)
	<-emitDone
	// The burst drowned the owner socket's bounded queue — it was
	// killed (slow consumers never stall the turn). The watcher is a
	// fresh socket; the journal overflow stays a journal property.
	ws.Close()

	// Watch from -1 hits the truncated journal: resync_required, then
	// still attaches live so the tab sees the terminal while it
	// re-reads history over HTTP.
	ws2 := dial(t, h)
	sendCommand(t, ws2, fmt.Sprintf(`{"type":"turn.watch","requestId":"w1","conversationId":%q,"turnId":%q,"afterSeq":-1}`, convID, turnID))
	f = readFrame(t, ws2)
	if f.Type != "turn.resync_required" || f.RequestID != "w1" || f.TurnID != turnID || f.Seq != nil {
		t.Fatalf("gap watch %+v, want resync_required", f)
	}
	close(b.release)
	for {
		f = readFrame(t, ws2)
		if f.Type == "turn.completed" {
			return
		}
		if f.Type == "turn.failed" || f.Type == "error" {
			t.Fatalf("watcher terminal %+v", f)
		}
	}
}

func TestSocketAdmissionBound(t *testing.T) {
	h := newHub(t, stubSender{fn: func(_ context.Context, convID, _, _, _ string,
		_ event.TurnEventHandler) (appconversation.SendResult, error) {
		return appconversation.SendResult{ConversationID: convID}, nil
	}})
	// One server, maxSockets real connections — a full hub refuses the
	// next attach.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		ws, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		h.Attach(ws)
	}))
	defer srv.Close()
	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	socks := make([]*websocket.Conn, 0, maxSockets)
	defer func() {
		for _, ws := range socks {
			ws.Close()
		}
	}()
	for i := 0; i < maxSockets; i++ {
		ws, _, err := websocket.DefaultDialer.Dial(url, nil)
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		socks = append(socks, ws)
	}
	// Dial returns at handshake completion; the server registers the
	// socket in Attach right after. Wait for the bound instead of
	// assuming the last attach already landed.
	deadline := time.Now().Add(5 * time.Second)
	for !h.SocketFull() {
		if time.Now().After(deadline) {
			t.Fatal("hub at capacity must report full")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// The next upgrade still completes the HTTP handshake — Attach
	// closes it politely server-side.
	ws, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("over-cap dial: %v", err)
	}
	defer ws.Close()
	_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, _, err := ws.ReadMessage(); err == nil {
		t.Fatal("over-cap socket should be closed by the hub")
	}
}

func TestSendSuccessWithoutReceiptIsInternal(t *testing.T) {
	h := newHub(t, stubSender{fn: func(_ context.Context, convID, _, _, _ string,
		_ event.TurnEventHandler) (appconversation.SendResult, error) {
		// Engine bug: success without a committed user line.
		return appconversation.SendResult{ConversationID: convID, UserMessagePersisted: false}, nil
	}})
	ws := dial(t, h)
	sendCommand(t, ws, `{"type":"turn.send","requestId":"r","payload":{"content":"x"}}`)
	var f frame
	for f = readFrame(t, ws); f.Type != "turn.failed" && f.Type != "turn.completed"; f = readFrame(t, ws) {
	}
	if f.Type != "turn.failed" {
		t.Fatalf("success-without-receipt emitted completed: %+v", f)
	}
	var p terminalPayload
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.Status != 500 || p.Code != "internal" {
		t.Fatalf("expected 500/internal, got %d/%s", p.Status, p.Code)
	}
}

// Oversize mid-turn: the context cancels, the job's real receipt lands
// in the terminal, and seq stays contiguous — the dropped frame never
// consumed a sequence number.
func TestOversizeTerminalKeepsRealReceipt(t *testing.T) {
	huge := strings.Repeat("n", maxFrameBytes)
	tc, err := entity.NewToolCall("call_1", huge, "{}", enum.TooltypeOrigin)
	if err != nil {
		t.Fatal(err)
	}
	h := newHub(t, stubSender{fn: func(ctx context.Context, convID, _, _, _ string,
		onEvent event.TurnEventHandler) (appconversation.SendResult, error) {
		onEvent(event.NewToolCallEvent(tc))  // exceeds the frame cap
		onEvent(event.NewDeltaEvent("late")) // suppressed while overflowed
		<-ctx.Done()                         // the overflow cancels the job
		// The user line committed before cancellation — the terminal
		// must carry this real receipt, not a fabricated false.
		return appconversation.SendResult{ConversationID: convID, UserMessagePersisted: true},
			apperror.New(408, "send canceled", ctx.Err())
	}})
	ws := dial(t, h)
	sendCommand(t, ws, `{"type":"turn.send","requestId":"r","payload":{"content":"x"}}`)

	var f frame
	for f = readFrame(t, ws); f.Type != "turn.failed"; f = readFrame(t, ws) {
	}
	if *f.Seq != 1 {
		t.Fatalf("terminal seq %d — overflowed frame must not consume seq", *f.Seq)
	}
	var p terminalPayload
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.Status != 413 || p.Code != "input_too_large" {
		t.Fatalf("overflow terminal %d/%s, want 413/input_too_large", p.Status, p.Code)
	}
	if p.UserMessagePersisted == nil || !*p.UserMessagePersisted {
		t.Fatal("overflow terminal dropped the real persistence receipt")
	}
	if !p.ResyncRequired {
		t.Fatal("overflow terminal must mark resyncRequired")
	}
}

func TestWatchEvictsExpiredJournal(t *testing.T) {
	h := newHub(t, stubSender{fn: func(_ context.Context, convID, _, _, _ string,
		_ event.TurnEventHandler) (appconversation.SendResult, error) {
		return appconversation.SendResult{ConversationID: convID, UserMessagePersisted: true}, nil
	}})
	ws := dial(t, h)
	sendCommand(t, ws, `{"type":"turn.send","requestId":"s1","payload":{"content":"x"}}`)
	var convID, turnID string
	for {
		f := readFrame(t, ws)
		if f.Type == "turn.completed" {
			convID, turnID = f.ConversationID, f.TurnID
			break
		}
	}
	// Age the journal past retention — the next lookup must evict it,
	// not wait for the next admission to sweep.
	h.mu.Lock()
	h.turns[turnID].closedAt = time.Now().Add(-journalMaxAge - time.Second)
	h.mu.Unlock()

	sendCommand(t, ws, fmt.Sprintf(`{"type":"turn.watch","requestId":"w1","conversationId":%q,"turnId":%q,"afterSeq":-1}`, convID, turnID))
	f := readFrame(t, ws)
	if f.Type != "turn.resync_required" || f.RequestID != "w1" {
		t.Fatalf("expired journal watch %+v, want resync_required", f)
	}
}

func TestOversizeMetadataTerminatesTurn(t *testing.T) {
	// A provider-controlled string (tool name) big enough to blow the
	// frame cap must terminate only this turn — sanitized, no panic.
	huge := strings.Repeat("n", maxFrameBytes) // 1MiB name
	tc, err := entity.NewToolCall("call_1", huge, "{}", enum.TooltypeOrigin)
	if err != nil {
		t.Fatal(err)
	}
	h := newHub(t, stubSender{fn: func(_ context.Context, convID, _, _, _ string,
		onEvent event.TurnEventHandler) (appconversation.SendResult, error) {
		onEvent(event.NewToolCallEvent(tc))
		return appconversation.SendResult{ConversationID: convID, UserMessagePersisted: true}, nil
	}})
	ws := dial(t, h)
	sendCommand(t, ws, `{"type":"turn.send","requestId":"r","payload":{"content":"x"}}`)
	var f frame
	for f = readFrame(t, ws); f.Type != "turn.failed"; f = readFrame(t, ws) {
		if f.Type == "turn.completed" {
			t.Fatal("oversize turn reported completed")
		}
	}
	var p terminalPayload
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.Status != 413 || p.Code != "input_too_large" {
		t.Fatalf("oversize terminal %d/%s, want 413/input_too_large", p.Status, p.Code)
	}
	if strings.Contains(p.Message, huge[:100]) {
		t.Fatal("oversize payload leaked into the terminal message")
	}
}

func TestToolInputStreamsBoundedExact(t *testing.T) {
	// Input larger than any one frame streams as ordered chunks; it is
	// owner-visible content, not a reason to terminate the turn.
	input := strings.Repeat("i", maxFrameBytes+outboundChunkBytes)
	tc, _ := entity.NewToolCall("call_big", "write_file", input, enum.TooltypeOrigin)
	h := newHub(t, stubSender{fn: func(_ context.Context, convID, _, _, _ string,
		onEvent event.TurnEventHandler) (appconversation.SendResult, error) {
		onEvent(event.NewToolCallEvent(tc))
		return appconversation.SendResult{ConversationID: convID, UserMessagePersisted: true}, nil
	}})
	ws := dial(t, h)
	sendCommand(t, ws, `{"type":"turn.send","requestId":"r","payload":{"content":"x"}}`)

	var got strings.Builder
	inputFrames := 0
	for {
		f := readFrame(t, ws)
		if f.Type == "turn.completed" {
			break
		}
		if f.Type == "turn.failed" {
			t.Fatalf("large input terminated turn: %+v", f)
		}
		if f.Type == "turn.tool_call" {
			var meta map[string]any
			if err := json.Unmarshal(f.Payload, &meta); err != nil {
				t.Fatal(err)
			}
			if _, ok := meta["input"]; ok {
				t.Fatal("tool_call metadata carried unbounded input")
			}
		}
		if f.Type != "turn.tool_input" {
			continue
		}
		inputFrames++
		var p toolInputPayload
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if p.CallID != "call_big" || len(p.Text) > outboundChunkBytes {
			t.Fatalf("bad input chunk: id=%q bytes=%d", p.CallID, len(p.Text))
		}
		got.WriteString(p.Text)
	}
	if inputFrames < 2 {
		t.Fatalf("large input was not chunked: %d frames", inputFrames)
	}
	if got.String() != input {
		t.Fatalf("reassembled input differs: %d vs %d bytes", got.Len(), len(input))
	}
}

func TestToolOutputStreamsOrderedChunks(t *testing.T) {
	tc, _ := entity.NewToolCall("call_1", "read_file", "{}", enum.TooltypeOrigin)
	h := newHub(t, stubSender{fn: func(_ context.Context, convID, _, _, _ string,
		onEvent event.TurnEventHandler) (appconversation.SendResult, error) {
		// Reasoning stretch: three chunks → one phase frame.
		onEvent(event.NewReasoningEvent("a"))
		onEvent(event.NewReasoningEvent("b"))
		onEvent(event.NewReasoningEvent("c"))
		onEvent(event.NewDeltaEvent("text"))
		// New stretch after text → a second marker is correct.
		onEvent(event.NewReasoningEvent("d"))
		// Tool output is live content: every event chunk crosses in
		// order as its own frame — no coalescing, no dedupe.
		onEvent(event.NewToolCallEvent(tc))
		onEvent(event.NewToolOutputEvent(tc, "chunk1"))
		onEvent(event.NewToolOutputEvent(tc, ""))
		onEvent(event.NewToolOutputEvent(tc, "chunk2"))
		onEvent(event.NewToolOutputEvent(tc, "chunk3"))
		return appconversation.SendResult{ConversationID: convID, UserMessagePersisted: true}, nil
	}})
	ws := dial(t, h)
	sendCommand(t, ws, `{"type":"turn.send","requestId":"r","payload":{"content":"x"}}`)
	counts := map[string]int{}
	var seqs []int
	var out strings.Builder
	for {
		f := readFrame(t, ws)
		counts[f.Type]++
		if f.Type == "turn.completed" {
			break
		}
		if f.Seq != nil {
			seqs = append(seqs, *f.Seq)
		}
		if f.Type == "turn.tool_output" {
			var p toolOutputPayload
			if err := json.Unmarshal(f.Payload, &p); err != nil {
				t.Fatal(err)
			}
			if p.CallID != "call_1" {
				t.Fatalf("tool_output callId %q", p.CallID)
			}
			out.WriteString(p.Text)
		}
	}
	if counts["turn.phase"] != 2 {
		t.Fatalf("phase frames %d, want 2 (one per stretch)", counts["turn.phase"])
	}
	if counts["turn.tool_output"] != 3 {
		t.Fatalf("tool_output frames %d, want 3 (one per event chunk)", counts["turn.tool_output"])
	}
	if out.String() != "chunk1chunk2chunk3" {
		t.Fatalf("tool output reassembled %q", out.String())
	}
	for i, s := range seqs {
		if s != i {
			t.Fatalf("seq %v — want strict 0..n", seqs)
		}
	}
	if counts["turn.tool_call"] != 1 || counts["turn.delta"] != 1 {
		t.Fatalf("counts %v", counts)
	}
}

func TestReasoningSummaryStreamsOrderedChunks(t *testing.T) {
	// Official summaries are owner-visible content: every nonempty
	// event chunk crosses in order as its own frame set, split at the
	// byte bound; the stretch still coalesces to one phase marker.
	big := strings.Repeat("s", outboundChunkBytes) + "TAIL"
	h := newHub(t, stubSender{fn: func(_ context.Context, convID, _, _, _ string,
		onEvent event.TurnEventHandler) (appconversation.SendResult, error) {
		onEvent(event.NewReasoningSummaryEvent("part1 "))
		onEvent(event.NewReasoningSummaryEvent(""))
		onEvent(event.NewReasoningSummaryEvent(big))
		return appconversation.SendResult{ConversationID: convID, UserMessagePersisted: true}, nil
	}})
	ws := dial(t, h)
	sendCommand(t, ws, `{"type":"turn.send","requestId":"r","payload":{"content":"x"}}`)
	counts := map[string]int{}
	var seqs []int
	var out strings.Builder
	for {
		f := readFrame(t, ws)
		counts[f.Type]++
		if f.Type == "turn.completed" {
			break
		}
		if f.Seq != nil {
			seqs = append(seqs, *f.Seq)
		}
		if f.Type == "turn.reasoning" {
			var p reasoningPayload
			if err := json.Unmarshal(f.Payload, &p); err != nil {
				t.Fatal(err)
			}
			if len(p.Text) > outboundChunkBytes {
				t.Fatalf("reasoning chunk %d bytes exceeds bound", len(p.Text))
			}
			if !utf8.ValidString(p.Text) {
				t.Fatal("reasoning chunk split a UTF-8 sequence")
			}
			out.WriteString(p.Text)
		}
	}
	if counts["turn.phase"] != 1 {
		t.Fatalf("phase frames %d, want 1 (one per stretch)", counts["turn.phase"])
	}
	if counts["turn.reasoning"] != 3 {
		t.Fatalf("reasoning frames %d, want 3 (small + empty + split pair)", counts["turn.reasoning"])
	}
	if out.String() != "part1 "+big {
		t.Fatalf("summary reassembled %q", out.String())
	}
	for i, s := range seqs {
		if s != i {
			t.Fatalf("seq %v — want strict 0..n", seqs)
		}
	}
}

func TestToolOutputChunkingUTF8(t *testing.T) {
	// Tool output larger than the per-frame bound splits at the byte
	// cap without breaking a rune, and reassembles exactly.
	big := strings.Repeat("a", outboundChunkBytes) + strings.Repeat("世", 12000) // 36KiB of multibyte
	tc, _ := entity.NewToolCall("call_1", "read_file", "{}", enum.TooltypeOrigin)
	h := newHub(t, stubSender{fn: func(_ context.Context, convID, _, _, _ string,
		onEvent event.TurnEventHandler) (appconversation.SendResult, error) {
		onEvent(event.NewToolCallEvent(tc))
		onEvent(event.NewToolOutputEvent(tc, big))
		return appconversation.SendResult{UserMessagePersisted: true}, nil
	}})
	ws := dial(t, h)
	sendCommand(t, ws, `{"type":"turn.send","requestId":"r","payload":{"content":"x"}}`)

	var got strings.Builder
	frames := 0
	for {
		f := readFrame(t, ws)
		if f.Type == "turn.completed" {
			break
		}
		if f.Type != "turn.tool_output" {
			continue
		}
		frames++
		var p toolOutputPayload
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if len(p.Text) > outboundChunkBytes {
			t.Fatalf("tool_output chunk %d bytes exceeds bound", len(p.Text))
		}
		if !utf8.ValidString(p.Text) {
			t.Fatal("tool_output chunk split a UTF-8 sequence")
		}
		got.WriteString(p.Text)
	}
	if frames < 2 {
		t.Fatalf("expected split into >1 frame, got %d", frames)
	}
	if got.String() != big {
		t.Fatalf("reassembled tool output differs: %d vs %d bytes", got.Len(), len(big))
	}
}
