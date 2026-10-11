package wsconversation

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gorilla/websocket"

	appconversation "github.com/jahrulnr/nusallm/application/conversation"
	appjob "github.com/jahrulnr/nusallm/application/job"
	"github.com/jahrulnr/nusallm/infrastructure/config"
	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/event"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
	repoconversation "github.com/jahrulnr/nusallm/infrastructure/repository/conversation"
	repogateway "github.com/jahrulnr/nusallm/infrastructure/repository/gateway"
)

// readUntil reads frames until want arrives — an error or failed
// terminal while waiting means the command was rejected outright.
func readUntil(t *testing.T, ws *websocket.Conn, want string) frame {
	t.Helper()
	for {
		f := readFrame(t, ws)
		switch f.Type {
		case want:
			return f
		case "error", "turn.failed":
			t.Fatalf("waiting for %s: %+v", want, f)
		}
	}
}

// gatedTool blocks in Execute until released — the deterministic window
// in which steering commands act on a live turn. entered closes once.
type gatedTool struct {
	name    string
	entered chan struct{}
	release chan struct{}
}

func newGatedTool(name string) *gatedTool {
	return &gatedTool{name: name, entered: make(chan struct{}), release: make(chan struct{})}
}

func (g *gatedTool) Schema() valueobject.ToolSchema {
	s, _ := valueobject.NewToolSchema(g.name, "blocks until released", nil, true, enum.ToolPermissionAgent)
	return s
}

func (g *gatedTool) Execute(ctx context.Context, _ entity.ToolCall, _ dservice.ToolEmitter) (dservice.ToolResult, error) {
	select {
	case <-g.entered:
	default:
		close(g.entered)
	}
	select {
	case <-g.release:
		return dservice.ToolResult{Text: "tool output"}, nil
	case <-ctx.Done():
		return dservice.ToolResult{}, ctx.Err()
	}
}

// scriptSvc demands the scripted tool by call number, "done" otherwise,
// and records the last two message contents each call saw — where a
// folded steer must sit relative to the context record trailing it.
type scriptSvc struct {
	calls   int32
	toolFor map[int32]string
	seen    map[int32]string
	prev    map[int32]string
}

func (s *scriptSvc) Send(_ context.Context, conv *aggregate.Conversation, _ *aggregate.Gateway, _ entity.Model, _ event.TurnEventHandler) error {
	n := atomic.AddInt32(&s.calls, 1)
	if s.seen != nil {
		msgs := conv.Messages()
		s.seen[n] = msgs[len(msgs)-1].Content()
		if s.prev != nil && len(msgs) > 1 {
			s.prev[n] = msgs[len(msgs)-2].Content()
		}
	}
	var m entity.Message
	if name, ok := s.toolFor[n]; ok {
		m, _ = entity.NewMessage(enum.RoleAssistant, "need "+name)
		tc, err := entity.NewToolCall(fmt.Sprintf("call_%d", n), name, `{}`, enum.TooltypeOrigin)
		if err != nil {
			return err
		}
		m.AddToolCall(tc)
		m.SetFinishReason(enum.FinishReasonToolCalls)
	} else {
		m, _ = entity.NewMessage(enum.RoleAssistant, "done")
		m.SetFinishReason(enum.FinishReasonStop)
	}
	conv.Append(m)
	return nil
}

// gatedApp wires a real ConversationApp as the hub's Sender — the room
// "room" binds the scripted provider and the gated tools, so steering
// receipts run the real claim/persist lifecycle inside the send job.
func gatedApp(t *testing.T, svc *scriptSvc, tools map[string]*gatedTool) *appconversation.ConversationApp {
	t.Helper()
	gw, _ := aggregate.NewGateway("gw1", "stub", "http://unused", enum.StyleCompletion)
	gw.DisableStream()
	m, _ := entity.NewModel("m1", "gw1")
	gw.SetModels([]entity.Model{m})
	gateways := repogateway.NewGatewayRepository(t.TempDir(), nil)
	if err := gateways.Save(t.Context(), &gw); err != nil {
		t.Fatal(err)
	}
	convs := repoconversation.NewConversationRepository(t.TempDir())
	envTools := map[string]dservice.Tool{}
	schemas := []valueobject.ToolSchema{}
	for name, tool := range tools {
		envTools[name] = tool
		schemas = append(schemas, tool.Schema())
	}
	app := appconversation.New(convs, gateways,
		map[enum.Environment]appjob.EnvironmentDriver{enum.EnvironmentLocal: appjob.EnvironmentBundle{
			Services: map[enum.Style]dservice.TextModelService{enum.StyleCompletion: svc},
			Tools:    envTools,
		}},
		config.NewConfig(), nil, "")
	seed, _ := aggregate.NewConversation("room")
	seed.SetTools(schemas)
	seed.SetRoute("gw1")
	seed.SetModelID("m1")
	if err := convs.Save(t.Context(), seed); err != nil {
		t.Fatal(err)
	}
	return app
}

// Steer command surface: every malformed or mistargeted shape answers
// a synchronous error, never a turn.
func TestSteerCommandValidation(t *testing.T) {
	b := newBlockedTurn()
	h := newHub(t, stubSender{fn: b.send})
	ws := dial(t, h)

	longID := strings.Repeat("r", maxControlRequestIDBytes+1)
	cases := []struct {
		name string
		cmd  string
		code string
	}{
		{"steer with afterSeq", `{"type":"turn.steer","requestId":"s","conversationId":"room","turnId":"t_1","afterSeq":1,"payload":{"content":"x"}}`, "invalid_argument"},
		{"steer requestId over bound", fmt.Sprintf(`{"type":"turn.steer","requestId":%q,"conversationId":"room","turnId":"t_1","payload":{"content":"x"}}`, longID), "invalid_argument"},
		{"steer missing convId", `{"type":"turn.steer","requestId":"s","turnId":"t_1","payload":{"content":"x"}}`, "invalid_argument"},
		{"steer bad turnId", `{"type":"turn.steer","requestId":"s","conversationId":"room","turnId":"a/b","payload":{"content":"x"}}`, "invalid_argument"},
		{"steer missing payload", `{"type":"turn.steer","requestId":"s","conversationId":"room","turnId":"t_1"}`, "invalid_argument"},
		{"steer blank content", `{"type":"turn.steer","requestId":"s","conversationId":"room","turnId":"t_1","payload":{"content":"  "}}`, "invalid_argument"},
		{"steer unknown turn", `{"type":"turn.steer","requestId":"s","conversationId":"room","turnId":"t_ghost","payload":{"content":"x"}}`, "not_found"},
		{"recall with afterSeq", `{"type":"turn.steer_recall","requestId":"s","conversationId":"room","turnId":"t_1","afterSeq":0,"payload":{"inputId":"i"}}`, "invalid_argument"},
		{"recall requestId over bound", fmt.Sprintf(`{"type":"turn.steer_recall","requestId":%q,"conversationId":"room","turnId":"t_1","payload":{"inputId":"i"}}`, longID), "invalid_argument"},
		{"recall missing inputId", `{"type":"turn.steer_recall","requestId":"s","conversationId":"room","turnId":"t_1","payload":{}}`, "invalid_argument"},
		{"recall unknown turn", `{"type":"turn.steer_recall","requestId":"s","conversationId":"room","turnId":"t_ghost","payload":{"inputId":"i"}}`, "not_found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sendCommand(t, ws, tc.cmd)
			f := readFrame(t, ws)
			if f.Type != "error" || f.Code != tc.code {
				t.Fatalf("got %+v, want error/%s", f, tc.code)
			}
		})
	}

	// A live send turn: steering works, foreign/terminal/kind rejections
	// hold on the real path.
	sendCommand(t, ws, `{"type":"turn.send","requestId":"s1","conversationId":"room","payload":{"content":"x"}}`)
	f := readUntil(t, ws, "turn.opened")
	convID, turnID := f.ConversationID, f.TurnID
	<-b.started

	sendCommand(t, ws, fmt.Sprintf(`{"type":"turn.steer","requestId":"st-foreign","conversationId":"elsewhere","turnId":%q,"payload":{"content":"x"}}`, turnID))
	if f := readFrame(t, ws); f.Type != "error" || f.Code != "not_found" {
		t.Fatalf("foreign steer %+v", f)
	}
	sendCommand(t, ws, fmt.Sprintf(`{"type":"turn.steer_recall","requestId":"rc-none","conversationId":%q,"turnId":%q,"payload":{"inputId":"never-submitted"}}`, convID, turnID))
	if f := readFrame(t, ws); f.Type != "error" || f.Code != "not_found" {
		t.Fatalf("unknown inputId recall %+v", f)
	}

	// An edit turn in the same room does not take steering.
	sendCommand(t, ws, `{"type":"turn.edit","requestId":"e1","conversationId":"room","payload":{"messageId":"m_1","content":"y"}}`)
	editOpened := readUntil(t, ws, "turn.opened")
	_ = readUntil(t, ws, "turn.failed") // stub has no edit scripted — 400
	sendCommand(t, ws, fmt.Sprintf(`{"type":"turn.steer","requestId":"st-edit","conversationId":%q,"turnId":%q,"payload":{"content":"x"}}`, convID, editOpened.TurnID))
	if f := readFrame(t, ws); f.Type != "error" || f.Code != "not_found" {
		t.Fatalf("steer on finished edit %+v", f)
	}

	close(b.release)
	_ = readUntil(t, ws, "turn.completed")
	sendCommand(t, ws, fmt.Sprintf(`{"type":"turn.steer","requestId":"st-dead","conversationId":%q,"turnId":%q,"payload":{"content":"x"}}`, convID, turnID))
	if f := readFrame(t, ws); f.Type != "error" || f.Code != "not_found" {
		t.Fatalf("steer on terminal turn %+v", f)
	}
}

// turn.steer acks journaled and deduped by requestId; the lifetime
// bound rejects rather than truncates, and the terminal snapshot lists
// every submitted input's state without content.
func TestSteerAckDedupAndCapacity(t *testing.T) {
	b := newBlockedTurn()
	h := newHub(t, stubSender{fn: b.send})
	ws := dial(t, h)

	sendCommand(t, ws, `{"type":"turn.send","requestId":"s1","conversationId":"room","payload":{"content":"x"}}`)
	f := readUntil(t, ws, "turn.opened")
	convID, turnID := f.ConversationID, f.TurnID
	<-b.started

	steer := func(reqID, content string) {
		sendCommand(t, ws, fmt.Sprintf(`{"type":"turn.steer","requestId":%q,"conversationId":%q,"turnId":%q,"payload":{"content":%q}}`, reqID, convID, turnID, content))
	}
	steer("st-0", "first")
	f = readFrame(t, ws)
	if f.Type != "turn.steer_ack" || f.TurnID != turnID || f.Seq == nil {
		t.Fatalf("ack %+v — want sequenced turn.steer_ack", f)
	}
	var ack steerInputPayload
	if err := json.Unmarshal(f.Payload, &ack); err != nil || ack.InputID != "st-0" {
		t.Fatalf("ack payload %s", f.Payload)
	}
	// Same id + same content: idempotent re-ack.
	steer("st-0", "first")
	if f = readFrame(t, ws); f.Type != "turn.steer_ack" {
		t.Fatalf("dedup resubmit %+v — want re-ack", f)
	}
	// Same id + different content: invalid, no third record.
	steer("st-0", "different")
	if f = readFrame(t, ws); f.Type != "error" || f.Code != "invalid_argument" {
		t.Fatalf("requestId reuse with new content %+v", f)
	}

	// Fill the lifetime bound: 1 + 31 = 32 records.
	for i := 1; i < maxSteerInputs; i++ {
		steer(fmt.Sprintf("st-%d", i), "x")
	}
	acks := 0
	for acks < maxSteerInputs-1 {
		if f = readFrame(t, ws); f.Type == "turn.steer_ack" {
			acks++
		} else {
			t.Fatalf("fill %+v — want acks", f)
		}
	}
	// The 33rd rejects — the bound refuses, never truncates.
	steer("st-over", "x")
	if f = readFrame(t, ws); f.Type != "error" || f.Code != "queue_full" {
		t.Fatalf("over-bound steer %+v, want queue_full", f)
	}

	close(b.release)
	f = readUntil(t, ws, "turn.completed")
	var p terminalPayload
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Steering) != maxSteerInputs {
		t.Fatalf("terminal snapshot = %d entries, want %d", len(p.Steering), maxSteerInputs)
	}
	for i, st := range p.Steering {
		wantID := fmt.Sprintf("st-%d", i)
		if st.InputID != wantID || st.State != "pending" {
			t.Fatalf("snapshot[%d] = %+v, want %s/pending — stub never consumes", i, st, wantID)
		}
	}
	if strings.Contains(string(f.Payload), "first") && strings.Contains(string(f.Payload), `"content"`) {
		t.Fatalf("snapshot must not repeat content: %s", f.Payload)
	}
}

// Recall withdraws a pending input exactly once — the journaled
// turn.steer_recalled answers the command and idempotent repeats.
func TestSteerRecallPendingAndDedup(t *testing.T) {
	b := newBlockedTurn()
	h := newHub(t, stubSender{fn: b.send})
	ws := dial(t, h)

	sendCommand(t, ws, `{"type":"turn.send","requestId":"s1","conversationId":"room","payload":{"content":"x"}}`)
	f := readUntil(t, ws, "turn.opened")
	convID, turnID := f.ConversationID, f.TurnID
	<-b.started

	sendCommand(t, ws, fmt.Sprintf(`{"type":"turn.steer","requestId":"st-1","conversationId":%q,"turnId":%q,"payload":{"content":"take this back"}}`, convID, turnID))
	_ = readUntil(t, ws, "turn.steer_ack")

	recall := func() frame {
		sendCommand(t, ws, fmt.Sprintf(`{"type":"turn.steer_recall","requestId":"rc","conversationId":%q,"turnId":%q,"payload":{"inputId":"st-1"}}`, convID, turnID))
		return readFrame(t, ws)
	}
	f = recall()
	if f.Type != "turn.steer_recalled" || f.TurnID != turnID || f.Seq == nil {
		t.Fatalf("recall %+v — want sequenced turn.steer_recalled", f)
	}
	var recalled steerInputPayload
	if err := json.Unmarshal(f.Payload, &recalled); err != nil || recalled.InputID != "st-1" {
		t.Fatalf("recalled payload %s", f.Payload)
	}
	// Idempotent — the same withdrawn input re-answers successfully.
	if f = recall(); f.Type != "turn.steer_recalled" {
		t.Fatalf("recall dedup %+v — want another turn.steer_recalled", f)
	}

	close(b.release)
	f = readUntil(t, ws, "turn.completed")
	var p terminalPayload
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Steering) != 1 || p.Steering[0].InputID != "st-1" || p.Steering[0].State != "recalled" {
		t.Fatalf("terminal snapshot %+v — want st-1/recalled", p.Steering)
	}
}

// A fold whose text matches an unconsumed submission still emits no
// inputId — receipt-free folds (CLI origin) never adopt a pending web
// receipt, and the chunk boundary lands on the last piece.
func TestSteerFrameWithoutConsumedReceipt(t *testing.T) {
	emit := make(chan event.TurnEvent, 8)
	release := make(chan struct{})
	h := newHub(t, stubSender{fn: func(ctx context.Context, convID, _, _, _ string,
		onEvent event.TurnEventHandler) (appconversation.SendResult, error) {
		for {
			select {
			case ev := <-emit:
				onEvent(ev)
			case <-release:
				return appconversation.SendResult{ConversationID: convID, UserMessagePersisted: true}, nil
			case <-ctx.Done():
				return appconversation.SendResult{ConversationID: convID}, ctx.Err()
			}
		}
	}})
	ws := dial(t, h)
	sendCommand(t, ws, `{"type":"turn.send","requestId":"s1","conversationId":"room","payload":{"content":"x"}}`)
	f := readUntil(t, ws, "turn.opened")
	convID, turnID := f.ConversationID, f.TurnID

	sendCommand(t, ws, fmt.Sprintf(`{"type":"turn.steer","requestId":"st-1","conversationId":%q,"turnId":%q,"payload":{"content":"same text"}}`, convID, turnID))
	_ = readUntil(t, ws, "turn.steer_ack")

	// The same text folds without that receipt ever consuming — the
	// frame carries no inputId, but the final boundary still crosses.
	emit <- event.NewSteerEvent("same text")
	f = readFrame(t, ws)
	if f.Type != "turn.steer" {
		t.Fatalf("steer frame %+v", f)
	}
	var p map[string]any
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if _, present := p["inputId"]; present {
		t.Fatalf("unconsumed receipt adopted by same-text fold: %s", f.Payload)
	}
	if p["text"] != "same text" || p["final"] != true {
		t.Fatalf("steer chunk %+v — want text + final:true", p)
	}
	close(release)
	_ = readUntil(t, ws, "turn.completed")
}

// A retained terminal turn answers a gapped watch with the stored
// verdict and receipt snapshot — never a bare frame.
func TestResyncCarriesOutcomeAndSnapshot(t *testing.T) {
	b := newBlockedTurn()
	h := newHub(t, stubSender{fn: b.send})
	ws := dial(t, h)

	sendCommand(t, ws, `{"type":"turn.send","requestId":"s1","conversationId":"room","payload":{"content":"x"}}`)
	f := readUntil(t, ws, "turn.opened")
	convID, turnID := f.ConversationID, f.TurnID
	<-b.started
	sendCommand(t, ws, fmt.Sprintf(`{"type":"turn.steer","requestId":"st-1","conversationId":%q,"turnId":%q,"payload":{"content":"pending forever"}}`, convID, turnID))
	_ = readUntil(t, ws, "turn.steer_ack")
	close(b.release)
	_ = readUntil(t, ws, "turn.completed")

	// A watch past the journal head on the retained terminal replays
	// the stored outcome: verdict, snapshot, terminal body.
	sendCommand(t, ws, fmt.Sprintf(`{"type":"turn.watch","requestId":"w1","conversationId":%q,"turnId":%q,"afterSeq":999}`, convID, turnID))
	f = readFrame(t, ws)
	if f.Type != "turn.resync_required" {
		t.Fatalf("gapped watch %+v", f)
	}
	var p resyncPayload
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.Turn != "completed" || p.Terminal == nil || p.Terminal.UserMessagePersisted == nil || !*p.Terminal.UserMessagePersisted {
		t.Fatalf("resync outcome %+v — want completed with the stored terminal", p)
	}
	if len(p.Steering) != 1 || p.Steering[0].InputID != "st-1" || p.Steering[0].State != "pending" {
		t.Fatalf("resync snapshot %+v", p.Steering)
	}
}

// A full app stack proves the exact-turn chain end to end: the submit
// lands on the exact turn's mailbox, folds at the safe point, the
// journaled turn.steer carries the inputId, a persisted receipt refuses
// recall, and the terminal snapshot reports every input's state.
func TestSteerExactTurnLifecycle(t *testing.T) {
	gate1, gate2 := newGatedTool("gate1"), newGatedTool("gate2")
	svc := &scriptSvc{toolFor: map[int32]string{1: "gate1", 2: "gate2"}, seen: map[int32]string{}, prev: map[int32]string{}}
	app := gatedApp(t, svc, map[string]*gatedTool{"gate1": gate1, "gate2": gate2})
	h := newHub(t, app)
	ws := dial(t, h)

	sendCommand(t, ws, `{"type":"turn.send","requestId":"s1","conversationId":"room","payload":{"content":"run it"}}`)
	f := readUntil(t, ws, "turn.opened")
	convID, turnID := f.ConversationID, f.TurnID
	<-gate1.entered // provider call 1 demanded gate1 — turn is live

	sendCommand(t, ws, fmt.Sprintf(`{"type":"turn.steer","requestId":"st-1","conversationId":%q,"turnId":%q,"payload":{"content":"hold on"}}`, convID, turnID))
	f = readFrame(t, ws)
	if f.Type != "turn.steer_ack" || f.TurnID != turnID {
		t.Fatalf("ack %+v", f)
	}
	close(gate1.release)

	// The fold lands before provider call 2: a sequenced turn.steer
	// carrying the submission's inputId and the final boundary.
	f = readUntil(t, ws, "turn.steer")
	var sp map[string]any
	if err := json.Unmarshal(f.Payload, &sp); err != nil {
		t.Fatal(err)
	}
	if sp["inputId"] != "st-1" || sp["text"] != "hold on" || sp["final"] != true {
		t.Fatalf("folded steer frame %s — want inputId/text/final", f.Payload)
	}
	<-gate2.entered
	if svc.prev[2] != "hold on" || svc.seen[2] != "" {
		t.Fatalf("provider call 2 saw prev=%q last=%q, want the folded steer then its context record", svc.prev[2], svc.seen[2])
	}

	// Persisted input refuses recall while the turn still runs.
	sendCommand(t, ws, fmt.Sprintf(`{"type":"turn.steer_recall","requestId":"rc-1","conversationId":%q,"turnId":%q,"payload":{"inputId":"st-1"}}`, convID, turnID))
	if f = readFrame(t, ws); f.Type != "error" || f.Code != "not_found" {
		t.Fatalf("persisted recall %+v — want not_found", f)
	}

	// A pending input recalls; it never folds.
	sendCommand(t, ws, fmt.Sprintf(`{"type":"turn.steer","requestId":"st-2","conversationId":%q,"turnId":%q,"payload":{"content":"scratch that"}}`, convID, turnID))
	_ = readUntil(t, ws, "turn.steer_ack")
	sendCommand(t, ws, fmt.Sprintf(`{"type":"turn.steer_recall","requestId":"rc-2","conversationId":%q,"turnId":%q,"payload":{"inputId":"st-2"}}`, convID, turnID))
	f = readFrame(t, ws)
	if f.Type != "turn.steer_recalled" {
		t.Fatalf("pending recall %+v", f)
	}
	close(gate2.release)

	f = readUntil(t, ws, "turn.completed")
	var term terminalPayload
	if err := json.Unmarshal(f.Payload, &term); err != nil {
		t.Fatal(err)
	}
	want := []steerStatePayload{
		{InputID: "st-1", State: "persisted"},
		{InputID: "st-2", State: "recalled"},
	}
	if fmt.Sprint(term.Steering) != fmt.Sprint(want) {
		t.Fatalf("terminal snapshot %+v, want %+v", term.Steering, want)
	}
	for n, last := range svc.seen {
		if last == "scratch that" || svc.prev[n] == "scratch that" {
			t.Fatalf("provider call %d saw the recalled input fold", n)
		}
	}
}

// A steer addressed to a queued send turn lands on ITS mailbox — never
// on the turn currently running — and folds when that job starts.
func TestSteerTargetsQueuedTurn(t *testing.T) {
	gate := newGatedTool("gate")
	svc := &scriptSvc{toolFor: map[int32]string{1: "gate"}, seen: map[int32]string{}}
	app := gatedApp(t, svc, map[string]*gatedTool{"gate": gate})
	h := newHub(t, app)
	ws := dial(t, h)

	sendCommand(t, ws, `{"type":"turn.send","requestId":"sA","conversationId":"room","payload":{"content":"first turn"}}`)
	f := readUntil(t, ws, "turn.opened")
	convID, turnA := f.ConversationID, f.TurnID
	<-gate.entered

	// Turn B queues behind A on the room worker — admitted, not running.
	sendCommand(t, ws, `{"type":"turn.send","requestId":"sB","conversationId":"room","payload":{"content":"second turn"}}`)
	fB := readUntil(t, ws, "turn.opened")
	turnB := fB.TurnID

	// Steering names B exactly — it must not touch A's mailbox.
	sendCommand(t, ws, fmt.Sprintf(`{"type":"turn.steer","requestId":"st-B","conversationId":%q,"turnId":%q,"payload":{"content":"for the queued job"}}`, convID, turnB))
	f = readFrame(t, ws)
	if f.Type != "turn.steer_ack" || f.TurnID != turnB {
		t.Fatalf("queued-turn ack %+v — must answer on turn B", f)
	}

	close(gate.release)
	_ = readUntil(t, ws, "turn.completed") // A finishes; B's job starts

	// B folds the mailbox at step zero — the frame lands on B with the
	// submission's inputId.
	var sp map[string]any
	for {
		f = readFrame(t, ws)
		if f.Type == "turn.steer" && f.TurnID == turnB {
			if err := json.Unmarshal(f.Payload, &sp); err != nil {
				t.Fatal(err)
			}
			break
		}
		if f.Type == "turn.completed" && f.TurnID == turnB {
			t.Fatal("B completed without folding the queued steer")
		}
		if f.Type == "turn.steer" && f.TurnID == turnA {
			t.Fatal("the queued steer folded into A — the running turn is not the target")
		}
	}
	if sp["inputId"] != "st-B" || sp["text"] != "for the queued job" {
		t.Fatalf("queued-turn steer frame %s", f.Payload)
	}
	f = readUntil(t, ws, "turn.completed")
	var term terminalPayload
	if err := json.Unmarshal(f.Payload, &term); err != nil {
		t.Fatal(err)
	}
	if len(term.Steering) != 1 || term.Steering[0].InputID != "st-B" || term.Steering[0].State != "persisted" {
		t.Fatalf("B's terminal snapshot %+v", term.Steering)
	}
}
