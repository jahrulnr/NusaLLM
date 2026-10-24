package wsconversation

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"

	appconversation "github.com/jahrulnr/nusallm/application/conversation"
	appjob "github.com/jahrulnr/nusallm/application/job"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/event"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
)

// stubSender scripts the application send/edit — the hub boundary is
// tested against its contract, not the provider stack.
type stubSender struct {
	fn func(ctx context.Context, convID, route, modelID, message string,
		onEvent event.TurnEventHandler) (appconversation.SendResult, error)
	optsFn func(appjob.SendOptions)
	efn    func(ctx context.Context, convID, route, modelID, msgID, content string,
		opts appjob.SendOptions, onEvent event.TurnEventHandler) error
}

func (s stubSender) Send(ctx context.Context, convID, route, modelID, message string,
	_ []valueobject.Attachment, opts appjob.SendOptions, onEvent event.TurnEventHandler) (appconversation.SendResult, error) {
	if s.optsFn != nil {
		s.optsFn(opts)
	}
	return s.fn(ctx, convID, route, modelID, message, onEvent)
}

func TestTurnSendPassesEnvironmentSelection(t *testing.T) {
	selected := make(chan appjob.SendOptions, 1)
	h := newHub(t, stubSender{
		fn: func(_ context.Context, id, _, _, _ string, _ event.TurnEventHandler) (appconversation.SendResult, error) {
			return appconversation.SendResult{ConversationID: id, UserMessagePersisted: true}, nil
		},
		optsFn: func(opts appjob.SendOptions) { selected <- opts },
	})
	ws := dial(t, h)
	sendCommand(t, ws, `{"type":"turn.send","requestId":"r","payload":{"content":"x","environment":"docker","workspace":"/remote/project"}}`)
	for f := readFrame(t, ws); f.Type != "turn.completed"; f = readFrame(t, ws) {
		if f.Type == "error" || f.Type == "turn.failed" {
			t.Fatalf("turn rejected: %+v", f)
		}
	}
	opts := <-selected
	if opts.Environment != enum.EnvironmentDocker || opts.Workspace != "/remote/project" {
		t.Fatalf("forwarded opts = %+v", opts)
	}
}

func (s stubSender) Edit(ctx context.Context, convID, route, modelID, msgID, content string,
	opts appjob.SendOptions, onEvent event.TurnEventHandler) error {
	if s.efn == nil {
		return apperror.New(400, "no edit scripted", nil)
	}
	return s.efn(ctx, convID, route, modelID, msgID, content, opts, onEvent)
}

// The edit path forwards the picker's per-turn selection — a retry must
// reach the model the user switched to, not the binding that failed.
func TestTurnEditPassesSelection(t *testing.T) {
	var gotRoute, gotModel string
	var gotOpts appjob.SendOptions
	h := newHub(t, stubSender{
		fn: func(context.Context, string, string, string, string, event.TurnEventHandler) (appconversation.SendResult, error) {
			return appconversation.SendResult{UserMessagePersisted: true}, nil
		},
		efn: func(_ context.Context, _, route, modelID, _, _ string,
			opts appjob.SendOptions, _ event.TurnEventHandler) error {
			gotRoute, gotModel, gotOpts = route, modelID, opts
			return nil
		},
	})
	ws := dial(t, h)
	sendCommand(t, ws, `{"type":"turn.edit","requestId":"e","conversationId":"c_1","payload":{"messageId":"m_1","content":"x","route":"gw2","modelId":"claude-y","effort":"high","provider":"or-fast","workspace":"/remote/project"}}`)
	for f := readFrame(t, ws); f.Type != "turn.completed"; f = readFrame(t, ws) {
		if f.Type == "error" || f.Type == "turn.failed" {
			t.Fatalf("turn rejected: %+v", f)
		}
	}
	if gotRoute != "gw2" || gotModel != "claude-y" ||
		gotOpts.Effort != enum.EffortHigh || gotOpts.Upstream != "or-fast" || gotOpts.Workspace != "/remote/project" {
		t.Fatalf("forwarded selection = %q %q %+v", gotRoute, gotModel, gotOpts)
	}
}

// An unknown effort word is a syntactic rejection on edit, same as send.
func TestTurnEditRejectsBadEffort(t *testing.T) {
	h := newHub(t, stubSender{})
	ws := dial(t, h)
	sendCommand(t, ws, `{"type":"turn.edit","requestId":"e","conversationId":"c_1","payload":{"messageId":"m_1","content":"x","effort":"ludicrous"}}`)
	if f := readFrame(t, ws); f.Type != "error" || f.Code != "invalid_argument" {
		t.Fatalf("bad effort %+v", f)
	}
}

// frame is the test-side view of a server envelope.
type frame struct {
	Type           string          `json:"type"`
	RequestID      string          `json:"requestId"`
	ConversationID string          `json:"conversationId"`
	TurnID         string          `json:"turnId"`
	Seq            *int            `json:"seq"`
	Payload        json.RawMessage `json:"payload"`
	Code           string          `json:"code"`
	Message        string          `json:"message"`
}

func newHub(t *testing.T, sender Sender) *Hub {
	t.Helper()
	h := NewHub(t.Context(), sender)
	t.Cleanup(h.Shutdown)
	return h
}

// dial upgrades a raw socket onto the hub — the upgrade edge itself is
// covered by the root handler tests; here the hub is under test.
func dial(t *testing.T, h *Hub) *websocket.Conn {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		ws, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		h.Attach(ws)
	}))
	t.Cleanup(srv.Close)
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ws.Close() })
	return ws
}

// readFrame reads the next turn-stream or command frame, stepping over
// conversation.activity broadcasts — they interleave freely with the
// frames a test waits on, and asserting stream order must not depend on
// the room-level notices.
func readFrame(t *testing.T, ws *websocket.Conn) frame {
	t.Helper()
	for {
		f := readAnyFrame(t, ws)
		if f.Type != "conversation.activity" {
			return f
		}
	}
}

// readAnyFrame reads the next frame verbatim — broadcasts included.
func readAnyFrame(t *testing.T, ws *websocket.Conn) frame {
	t.Helper()
	_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, raw, err := ws.ReadMessage()
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	var f frame
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("frame not JSON: %v", err)
	}
	return f
}

func sendCommand(t *testing.T, ws *websocket.Conn, cmd string) {
	t.Helper()
	_ = ws.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := ws.WriteMessage(websocket.TextMessage, []byte(cmd)); err != nil {
		t.Fatalf("send command: %v", err)
	}
}

func TestTurnSendHappyPath(t *testing.T) {
	toolCall, err := entity.NewToolCall("call_1", "read_file", `{"path":"/secret/x"}`, enum.TooltypeOrigin)
	if err != nil {
		t.Fatal(err)
	}
	h := newHub(t, stubSender{fn: func(_ context.Context, convID, route, modelID, message string,
		onEvent event.TurnEventHandler) (appconversation.SendResult, error) {
		if route != "gw1" || modelID != "m1" || message != "hi" {
			return appconversation.SendResult{UserMessagePersisted: true}, fmt.Errorf("bad send args %q %q %q", route, modelID, message)
		}
		onEvent(event.NewDeltaEvent("hello"))
		onEvent(event.NewReasoningEvent("PRIVATE-CHAIN-OF-THOUGHT"))
		onEvent(event.NewReasoningSummaryEvent("OFFICIAL-SUMMARY-PART-1 "))
		onEvent(event.NewReasoningSummaryEvent("OFFICIAL-SUMMARY-PART-2"))
		onEvent(event.NewToolCallEvent(toolCall))
		outCall, _ := entity.NewToolCall("call_1", "read_file", "{}", enum.TooltypeOrigin)
		onEvent(event.NewToolOutputEvent(outCall, "SECRET-TOOL-OUTPUT"))
		onEvent(event.NewSteerEvent("USER-STEER-LINE"))
		usage, _ := valueobject.NewUsage(10, 0, 0, 5)
		onEvent(event.NewFinishedEvent(enum.FinishReasonStop, usage))
		onEvent(event.NewErrorEvent(fmt.Errorf("transient retry — never crosses")))
		return appconversation.SendResult{ConversationID: convID, UserMessagePersisted: true}, nil
	}})
	ws := dial(t, h)

	sendCommand(t, ws, `{"type":"turn.send","requestId":"req-1","payload":{"route":"gw1","modelId":"m1","content":"hi"}}`)

	var seqs []int
	var types []string
	var sawRaw strings.Builder
	var term frame
	for {
		_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, raw, err := ws.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		sawRaw.Write(raw)
		var f frame
		if err := json.Unmarshal(raw, &f); err != nil {
			t.Fatalf("frame not JSON: %v", err)
		}
		types = append(types, f.Type)
		if f.Type == "conversation.activity" {
			// The room-level broadcast rides the same socket —
			// unsequenced, outside the turn's ordered record.
			if f.Seq != nil {
				t.Fatalf("broadcast carried seq: %+v", f)
			}
			continue
		}
		if f.Type != "error" {
			if f.Seq == nil {
				t.Fatalf("%s frame missing seq", f.Type)
			}
			seqs = append(seqs, *f.Seq)
			if !strings.HasPrefix(f.ConversationID, "c_") || !strings.HasPrefix(f.TurnID, "t_") {
				t.Fatalf("envelope: %+v", f)
			}
			// requestId rides only opened/terminal — stream frames
			// correlate by turnId, never by requestId.
			wantReq := f.Type == "turn.opened" || f.Type == "turn.completed" || f.Type == "turn.failed"
			if (f.RequestID == "req-1") != wantReq {
				t.Fatalf("%s requestId=%q — want present=%v", f.Type, f.RequestID, wantReq)
			}
		}
		if f.Type == "turn.completed" || f.Type == "turn.failed" {
			term = f
			break
		}
	}

	want := []string{"turn.opened", "conversation.activity", "turn.delta", "turn.phase", "turn.reasoning", "turn.reasoning", "turn.reasoning", "turn.tool_call", "turn.tool_input", "turn.tool_output", "turn.steer", "turn.step_finished", "turn.completed"}
	if fmt.Sprint(types) != fmt.Sprint(want) {
		t.Fatalf("frame types %v, want %v", types, want)
	}
	for i, s := range seqs {
		if s != i {
			t.Fatalf("seq %v — want strict 0..n", seqs)
		}
	}
	// Provider content crosses exactly in arrival order; error causes do not.
	if !strings.Contains(sawRaw.String(), `"text":"hello"`) {
		t.Fatalf("delta text missing: %s", sawRaw.String())
	}
	if !strings.Contains(sawRaw.String(), `"text":"PRIVATE-CHAIN-OF-THOUGHT"`) {
		t.Fatalf("raw reasoning missing: %s", sawRaw.String())
	}
	for _, leak := range []string{"transient retry"} {
		if strings.Contains(sawRaw.String(), leak) {
			t.Fatalf("frame leaked %q", leak)
		}
	}
	p1 := strings.Index(sawRaw.String(), `"text":"OFFICIAL-SUMMARY-PART-1 "`)
	p2 := strings.Index(sawRaw.String(), `"text":"OFFICIAL-SUMMARY-PART-2"`)
	raw := strings.Index(sawRaw.String(), `"text":"PRIVATE-CHAIN-OF-THOUGHT"`)
	if raw < 0 || p1 < 0 || p2 < 0 || !(raw < p1 && p1 < p2) {
		t.Fatalf("reasoning chunks missing or out of order: %s", sawRaw.String())
	}
	if !strings.Contains(sawRaw.String(), `"callId":"call_1"`) ||
		!strings.Contains(sawRaw.String(), `"name":"read_file"`) ||
		!strings.Contains(sawRaw.String(), `"text":"{\"path\":\"/secret/x\"}"`) ||
		!strings.Contains(sawRaw.String(), `"text":"SECRET-TOOL-OUTPUT"`) ||
		!strings.Contains(sawRaw.String(), `"text":"USER-STEER-LINE"`) ||
		!strings.Contains(sawRaw.String(), `"finishReason":"stop"`) ||
		!strings.Contains(sawRaw.String(), `"totalTokens":15`) {
		t.Fatalf("expected projection fields missing: %s", sawRaw.String())
	}
	var termBody terminalPayload
	if err := json.Unmarshal(term.Payload, &termBody); err != nil {
		t.Fatal(err)
	}
	if termBody.UserMessagePersisted == nil || !*termBody.UserMessagePersisted || !termBody.ResyncRequired {
		t.Fatalf("terminal payload: %+v", termBody)
	}
}

func TestTurnSendValidation(t *testing.T) {
	h := newHub(t, stubSender{fn: func(context.Context, string, string, string, string,
		event.TurnEventHandler) (appconversation.SendResult, error) {
		return appconversation.SendResult{UserMessagePersisted: true}, nil
	}})
	ws := dial(t, h)

	cases := []struct {
		name    string
		cmd     string
		code    string
		reqEcho string // requestId echoed back on the error frame
	}{
		{"malformed json", `{"type":`, "invalid_argument", ""},
		{"two documents", `{"type":"turn.send","requestId":"r","payload":{"content":"x"}} {}`, "invalid_argument", ""},
		{"unknown field", `{"type":"turn.send","requestId":"r","bogus":1,"payload":{"content":"x"}}`, "invalid_argument", "r"},
		{"missing requestId", `{"type":"turn.send","payload":{"content":"x"}}`, "invalid_argument", ""},
		{"blank requestId", `{"type":"turn.send","requestId":"  ","payload":{"content":"x"}}`, "invalid_argument", ""},
		{"send with turnId", `{"type":"turn.send","requestId":"r","turnId":"t_1","payload":{"content":"x"}}`, "invalid_argument", "r"},
		{"send with afterSeq", `{"type":"turn.send","requestId":"r","afterSeq":3,"payload":{"content":"x"}}`, "invalid_argument", "r"},
		{"missing payload", `{"type":"turn.send","requestId":"r"}`, "invalid_argument", "r"},
		{"payload unknown field", `{"type":"turn.send","requestId":"r","payload":{"content":"x","extra":1}}`, "invalid_argument", "r"},
		{"unknown environment", `{"type":"turn.send","requestId":"r","payload":{"content":"x","environment":"cloud"}}`, "invalid_argument", "r"},
		{"bad conversationId", `{"type":"turn.send","requestId":"r","conversationId":"a/b","payload":{"content":"x"}}`, "invalid_argument", "r"},
		{"dotdot conversationId", `{"type":"turn.send","requestId":"r","conversationId":"..","payload":{"content":"x"}}`, "invalid_argument", "r"},
		{"unknown command", `{"type":"convo.list","requestId":"r"}`, "invalid_argument", "r"},
		{"edit missing convId", `{"type":"turn.edit","requestId":"r","payload":{"messageId":"m_1","content":"x"}}`, "invalid_argument", "r"},
		{"edit with environment", `{"type":"turn.edit","requestId":"r","conversationId":"c_1","payload":{"messageId":"m_1","content":"x","environment":"local"}}`, "invalid_argument", "r"},
		{"edit bad msgId", `{"type":"turn.edit","requestId":"r","conversationId":"c_1","payload":{"messageId":"a/b","content":"x"}}`, "invalid_argument", "r"},
		{"cancel missing convId", `{"type":"turn.cancel","requestId":"r","turnId":"t_1"}`, "invalid_argument", "r"},
		{"cancel bad turnId", `{"type":"turn.cancel","requestId":"r","conversationId":"c_1","turnId":"a/b"}`, "invalid_argument", "r"},
		{"watch missing afterSeq", `{"type":"turn.watch","requestId":"r","conversationId":"c_1","turnId":"t_1"}`, "invalid_argument", "r"},
		{"watch afterSeq below floor", `{"type":"turn.watch","requestId":"r","conversationId":"c_1","turnId":"t_1","afterSeq":-2}`, "invalid_argument", "r"},
		{"literal null", `null`, "invalid_argument", ""},
		{"trailing junk", `{"type":"turn.send","requestId":"r","payload":{"content":"x"}}junk`, "invalid_argument", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sendCommand(t, ws, tc.cmd)
			f := readFrame(t, ws)
			if f.Type != "error" || f.Code != tc.code {
				t.Fatalf("got %+v, want error/%s", f, tc.code)
			}
			if f.RequestID != tc.reqEcho {
				t.Fatalf("requestId echo %q, want %q", f.RequestID, tc.reqEcho)
			}
		})
	}
}

func TestConversationIDBinding(t *testing.T) {
	var gotConv string
	h := newHub(t, stubSender{fn: func(_ context.Context, convID, _, _, _ string,
		_ event.TurnEventHandler) (appconversation.SendResult, error) {
		gotConv = convID
		return appconversation.SendResult{ConversationID: convID, UserMessagePersisted: true}, nil
	}})
	ws := dial(t, h)

	// Supplied valid id flows through untouched.
	sendCommand(t, ws, `{"type":"turn.send","requestId":"r1","conversationId":"my-room","payload":{"content":"x"}}`)
	for f := readFrame(t, ws); f.Type != "turn.completed"; f = readFrame(t, ws) {
		if f.ConversationID != "my-room" {
			t.Fatalf("conversationId not echoed: %+v", f)
		}
	}
	if gotConv != "my-room" {
		t.Fatalf("sender saw convID %q", gotConv)
	}
}

func TestTurnFailedClassification(t *testing.T) {
	cases := []struct {
		name          string
		err           error
		wantStatus    int
		wantCode      string
		wantPersisted bool
		absent        string
	}{
		{"upstream 429", apperror.FromStatus(429, "rate limited", fmt.Errorf("HTTP 429 quota")), 429, "upstream_error", true, "quota"},
		{"local queue 429", apperror.New(429, "conversation mailbox full", nil), 429, "queue_full", true, ""},
		{"bad model", apperror.New(400, "model not in catalog", fmt.Errorf("internal detail")), 400, "invalid_argument", false, "internal detail"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHub(t, stubSender{fn: func(_ context.Context, convID, _, _, _ string,
				_ event.TurnEventHandler) (appconversation.SendResult, error) {
				return appconversation.SendResult{ConversationID: convID, UserMessagePersisted: tc.wantPersisted}, tc.err
			}})
			ws := dial(t, h)
			sendCommand(t, ws, `{"type":"turn.send","requestId":"r","payload":{"content":"x"}}`)
			var f frame
			for f = readFrame(t, ws); f.Type != "turn.failed"; f = readFrame(t, ws) {
			}
			var p terminalPayload
			if err := json.Unmarshal(f.Payload, &p); err != nil {
				t.Fatal(err)
			}
			if p.Status != tc.wantStatus || p.Code != tc.wantCode {
				t.Fatalf("terminal %+v, want %d/%s", p, tc.wantStatus, tc.wantCode)
			}
			if p.UserMessagePersisted == nil || *p.UserMessagePersisted != tc.wantPersisted || !p.ResyncRequired {
				t.Fatalf("terminal payload: %+v", p)
			}
			if tc.absent != "" && strings.Contains(p.Message, tc.absent) {
				t.Fatalf("cause leaked into message: %q", p.Message)
			}
		})
	}
}

// Empty content is not an envelope error — the turn is admitted and the
// use case's 400 lands as turn.failed/invalid_argument (the app owns the
// submittable rule for both send and edit).
func TestEmptyContentRunsAsFailedTurn(t *testing.T) {
	bad := apperror.New(400, "empty message", fmt.Errorf("no content"))
	h := newHub(t, stubSender{
		fn: func(_ context.Context, convID, _, _, _ string,
			_ event.TurnEventHandler) (appconversation.SendResult, error) {
			return appconversation.SendResult{ConversationID: convID}, bad
		},
		efn: func(_ context.Context, _, _, _, _, _ string, _ appjob.SendOptions, _ event.TurnEventHandler) error {
			return bad
		},
	})
	ws := dial(t, h)

	for _, cmd := range []string{
		`{"type":"turn.send","requestId":"r","payload":{"content":""}}`,
		`{"type":"turn.send","requestId":"r","payload":{"content":"   "}}`,
		`{"type":"turn.edit","requestId":"r","conversationId":"c_1","payload":{"messageId":"m_1","content":""}}`,
	} {
		sendCommand(t, ws, cmd)
		var f frame
		for f = readFrame(t, ws); f.Type != "turn.failed"; f = readFrame(t, ws) {
			if f.Type == "error" {
				t.Fatalf("empty content must run as a failed turn, got sync error: %+v", f)
			}
		}
		var p terminalPayload
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if p.Status != 400 || p.Code != "invalid_argument" {
			t.Fatalf("terminal %+v, want 400/invalid_argument", p)
		}
	}
}

func TestQueueFullAdmission(t *testing.T) {
	release := make(chan struct{})
	var started sync.WaitGroup
	h := newHub(t, stubSender{fn: func(ctx context.Context, _ string, _, _, _ string,
		_ event.TurnEventHandler) (appconversation.SendResult, error) {
		started.Done()
		select {
		case <-release:
			return appconversation.SendResult{UserMessagePersisted: true}, nil
		case <-ctx.Done():
			return appconversation.SendResult{}, apperror.New(408, "send canceled", ctx.Err())
		}
	}})
	ws := dial(t, h)
	started.Add(maxActiveTurns)

	for i := 0; i < maxActiveTurns; i++ {
		sendCommand(t, ws, fmt.Sprintf(`{"type":"turn.send","requestId":"a-%d","payload":{"content":"x"}}`, i))
	}
	// All admitted: each emits opened. Wait for the workers to be live
	// before testing the 33rd, so the bound is exercised deterministically.
	for i := 0; i < maxActiveTurns; i++ {
		if f := readFrame(t, ws); f.Type != "turn.opened" {
			t.Fatalf("admission %d: %+v", i, f)
		}
	}
	started.Wait()
	sendCommand(t, ws, `{"type":"turn.send","requestId":"over","payload":{"content":"x"}}`)
	f := readFrame(t, ws)
	if f.Type != "error" || f.Code != "queue_full" || f.RequestID != "over" {
		t.Fatalf("got %+v, want queue_full rejection", f)
	}
	close(release)
	// Drain the terminal frames so the deferred Shutdown sees a quiet hub.
	for i := 0; i < maxActiveTurns; i++ {
		if f := readFrame(t, ws); f.Type != "turn.completed" {
			t.Fatalf("terminal %d: %+v", i, f)
		}
	}
}

func TestDisconnectDoesNotCancelTurn(t *testing.T) {
	started := make(chan context.Context, 1)
	done := make(chan struct{})
	h := newHub(t, stubSender{fn: func(ctx context.Context, _ string, _, _, _ string,
		_ event.TurnEventHandler) (appconversation.SendResult, error) {
		started <- ctx
		<-done
		return appconversation.SendResult{UserMessagePersisted: true}, nil
	}})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		ws, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		h.Attach(ws)
	}))
	defer srv.Close()
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	sendCommand(t, ws, `{"type":"turn.send","requestId":"r","payload":{"content":"x"}}`)
	var sendCtx context.Context
	select {
	case sendCtx = <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("send never started")
	}
	ws.Close() // owner socket drops mid-turn
	time.Sleep(50 * time.Millisecond)
	select {
	case <-sendCtx.Done():
		t.Fatal("socket disconnect canceled the turn")
	default:
	}
	close(done)
	// The turn completes on the journal even with no subscriber.
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.mu.Lock()
		var term bool
		for _, tr := range h.turns {
			term = term || tr.terminal
		}
		live := len(h.turns)
		h.mu.Unlock()
		if term || live == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("turn never completed after socket close")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestShutdownTerminalPreservesReceipt(t *testing.T) {
	release := make(chan struct{})
	h := newHub(t, stubSender{fn: func(ctx context.Context, convID string, _, _, _ string,
		_ event.TurnEventHandler) (appconversation.SendResult, error) {
		select {
		case <-release:
			return appconversation.SendResult{ConversationID: convID}, nil
		case <-ctx.Done():
			// User line already committed before shutdown canceled us —
			// the terminal must carry that receipt, not a fabricated one.
			return appconversation.SendResult{ConversationID: convID, UserMessagePersisted: true},
				apperror.New(408, "send canceled", ctx.Err())
		}
	}})
	ws := dial(t, h)
	sendCommand(t, ws, `{"type":"turn.send","requestId":"r","payload":{"content":"x"}}`)
	if f := readFrame(t, ws); f.Type != "turn.opened" {
		t.Fatalf("%+v", f)
	}
	go h.Shutdown()
	var f frame
	for f = readFrame(t, ws); f.Type != "turn.failed"; f = readFrame(t, ws) {
	}
	var p terminalPayload
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.Status != 503 || p.Code != "unavailable" {
		t.Fatalf("shutdown terminal %+v, want 503/unavailable", p)
	}
	if p.UserMessagePersisted == nil || !*p.UserMessagePersisted {
		t.Fatal("shutdown terminal dropped the real persistence receipt")
	}
	if !p.ResyncRequired {
		t.Fatal("shutdown terminal must mark resyncRequired")
	}
}

func TestShutdownNoFalseTerminalOnTimeout(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	var returned sync.WaitGroup
	returned.Add(1)
	h := newHub(t, stubSender{fn: func(ctx context.Context, convID string, _, _, _ string,
		_ event.TurnEventHandler) (appconversation.SendResult, error) {
		defer returned.Done()
		<-release // ignores cancellation until released
		return appconversation.SendResult{ConversationID: convID}, nil
	}})
	h.turnGrace = 100 * time.Millisecond
	h.drainGrace = 100 * time.Millisecond
	ws := dial(t, h)
	sendCommand(t, ws, `{"type":"turn.send","requestId":"r","payload":{"content":"x"}}`)
	if f := readFrame(t, ws); f.Type != "turn.opened" {
		t.Fatalf("%+v", f)
	}
	start := time.Now()
	h.Shutdown()
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("shutdown blocked %v on a stuck turn", elapsed)
	}
	// Socket is dead; whatever arrives next is the close handshake, and
	// no terminal was fabricated for the still-running send.
	_ = ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		_, raw, err := ws.ReadMessage()
		if err != nil {
			return
		}
		var f frame
		if json.Unmarshal(raw, &f) == nil && (f.Type == "turn.completed" || f.Type == "turn.failed") {
			t.Fatalf("terminal emitted for a turn that never returned: %s", raw)
		}
	}
}

func TestDeltaChunkingUTF8(t *testing.T) {
	// Chunks must stay within the byte bound and never split a rune.
	big := strings.Repeat("a", outboundChunkBytes) + strings.Repeat("世", 12000) // 36KiB of multibyte
	h := newHub(t, stubSender{fn: func(_ context.Context, _ string, _, _, _ string,
		onEvent event.TurnEventHandler) (appconversation.SendResult, error) {
		onEvent(event.NewDeltaEvent(big))
		return appconversation.SendResult{UserMessagePersisted: true}, nil
	}})
	ws := dial(t, h)
	sendCommand(t, ws, `{"type":"turn.send","requestId":"r","payload":{"content":"x"}}`)

	var got strings.Builder
	for {
		f := readFrame(t, ws)
		if f.Type == "turn.completed" {
			break
		}
		if f.Type != "turn.delta" {
			continue
		}
		var p deltaPayload
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if len(p.Text) > outboundChunkBytes {
			t.Fatalf("delta chunk %d bytes exceeds bound", len(p.Text))
		}
		if !utf8.ValidString(p.Text) {
			t.Fatal("delta chunk split a UTF-8 sequence")
		}
		got.WriteString(p.Text)
	}
	if got.String() != big {
		t.Fatalf("reassembled text differs: %d vs %d bytes", got.Len(), len(big))
	}
}

func TestSplitDelta(t *testing.T) {
	if got := splitDelta("", 8); len(got) != 0 {
		t.Fatalf("empty split: %v", got)
	}
	if got := splitDelta("abc", 8); len(got) != 1 || got[0] != "abc" {
		t.Fatalf("small split: %v", got)
	}
	// A rune straddling the boundary backs the cut off, not splits it.
	got := splitDelta("ab世界cd", 4)
	if fmt.Sprint(got) != fmt.Sprint([]string{"ab", "世", "界c", "d"}) {
		t.Fatalf("utf8 boundary split: %q", got)
	}
}

func TestSplitDeltaMalformedBytesStayBounded(t *testing.T) {
	// Tool output can be arbitrary bytes. Splitting must remain bounded
	// and must not rescan the whole prefix for every invalid byte.
	bad := string([]byte{0xff}) + strings.Repeat("a", outboundChunkBytes)
	chunks := splitDelta(bad, outboundChunkBytes)
	if len(chunks) != 2 || len(chunks[0]) != outboundChunkBytes {
		t.Fatalf("malformed split: %d chunks, first=%d", len(chunks), len(chunks[0]))
	}
	if strings.Join(chunks, "") != bad {
		t.Fatal("malformed split did not preserve content")
	}
}

func TestSlowSubscriberNeverStallsTurn(t *testing.T) {
	var done sync.WaitGroup
	done.Add(1)
	h := newHub(t, stubSender{fn: func(_ context.Context, _ string, _, _, _ string,
		onEvent event.TurnEventHandler) (appconversation.SendResult, error) {
		defer done.Done()
		for i := 0; i < outboundQueueCap*3; i++ {
			onEvent(event.NewDeltaEvent(strings.Repeat("x", 100)))
		}
		return appconversation.SendResult{UserMessagePersisted: true}, nil
	}})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		ws, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		h.Attach(ws)
	}))
	defer srv.Close()
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	sendCommand(t, ws, `{"type":"turn.send","requestId":"r","payload":{"content":"x"}}`)
	// Never read: the client is wedged. The turn must still complete —
	// the enqueue path kills the socket instead of blocking the sender.
	waitCh := make(chan struct{})
	go func() { done.Wait(); close(waitCh) }()
	select {
	case <-waitCh:
	case <-time.After(10 * time.Second):
		t.Fatal("turn stalled behind a dead subscriber")
	}
}

func TestOversizedInboundFrame(t *testing.T) {
	h := newHub(t, stubSender{fn: func(context.Context, string, string, string, string,
		event.TurnEventHandler) (appconversation.SendResult, error) {
		return appconversation.SendResult{UserMessagePersisted: true}, nil
	}})
	ws := dial(t, h)
	// 64KiB+1 — the server read limit closes the socket (1009).
	if err := ws.WriteMessage(websocket.TextMessage, make([]byte, maxInboundFrameBytes+1)); err != nil {
		t.Fatal(err)
	}
	_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _, err := ws.ReadMessage()
	if err == nil {
		t.Fatal("oversized frame should terminate the socket")
	}
}

func TestBinaryFrameRejected(t *testing.T) {
	h := newHub(t, stubSender{fn: func(context.Context, string, string, string, string,
		event.TurnEventHandler) (appconversation.SendResult, error) {
		return appconversation.SendResult{UserMessagePersisted: true}, nil
	}})
	ws := dial(t, h)
	if err := ws.WriteMessage(websocket.BinaryMessage, []byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	// Binary is answered by an error frame then the socket closes.
	var sawErr bool
	_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		_, raw, err := ws.ReadMessage()
		if err != nil {
			break
		}
		var f frame
		if json.Unmarshal(raw, &f) == nil && f.Type == "error" {
			sawErr = true
		}
	}
	if !sawErr {
		t.Fatal("binary frame should answer an error before close")
	}
}

func TestNoPostTerminalStream(t *testing.T) {
	var mu sync.Mutex
	var handler event.TurnEventHandler
	h := newHub(t, stubSender{fn: func(_ context.Context, _ string, _, _, _ string,
		onEvent event.TurnEventHandler) (appconversation.SendResult, error) {
		mu.Lock()
		handler = onEvent
		mu.Unlock()
		return appconversation.SendResult{UserMessagePersisted: true}, nil
	}})
	ws := dial(t, h)
	sendCommand(t, ws, `{"type":"turn.send","requestId":"r","payload":{"content":"x"}}`)
	var last frame
	for last = readFrame(t, ws); last.Type != "turn.completed"; last = readFrame(t, ws) {
	}
	// A late event after the terminal must not reach the socket. The
	// room-activity broadcast is the one legitimate follower — it is
	// not turn stream data.
	mu.Lock()
	if handler != nil {
		handler(event.NewDeltaEvent("late"))
	}
	mu.Unlock()
	_ = ws.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	for {
		_, raw, err := ws.ReadMessage()
		if err != nil {
			return
		}
		var f frame
		if json.Unmarshal(raw, &f) != nil || f.Type != "conversation.activity" {
			t.Fatalf("post-terminal stream frame arrived: %s", raw)
		}
	}
}

func TestActiveTurnOldestLive(t *testing.T) {
	release := make(chan struct{})
	h := newHub(t, stubSender{fn: func(ctx context.Context, id, _, _, _ string, _ event.TurnEventHandler) (appconversation.SendResult, error) {
		select {
		case <-release:
			return appconversation.SendResult{ConversationID: id, UserMessagePersisted: true}, nil
		case <-ctx.Done():
			return appconversation.SendResult{}, ctx.Err()
		}
	}})
	ws := dial(t, h)
	sendCommand(t, ws, `{"type":"turn.send","requestId":"r1","conversationId":"c_room","payload":{"content":"one"}}`)
	first := readFrame(t, ws)
	if first.Type != "turn.opened" {
		t.Fatalf("first: %+v", first)
	}
	sendCommand(t, ws, `{"type":"turn.send","requestId":"r2","conversationId":"c_room","payload":{"content":"two"}}`)
	second := readFrame(t, ws)
	if second.Type != "turn.opened" {
		t.Fatalf("second: %+v", second)
	}
	id, kind, ok := h.ActiveTurn("c_room")
	if !ok || id != first.TurnID || kind != "send" {
		t.Fatalf("active = %q %q %v, want %s send", id, kind, ok, first.TurnID)
	}
	if _, _, other := h.ActiveTurn("c_other"); other {
		t.Fatal("other room reported a turn")
	}
	close(release)
	got := 0
	for got < 2 {
		f := readFrame(t, ws)
		if f.Type == "turn.completed" {
			got++
			continue
		}
		if f.Type == "turn.failed" || f.Type == "error" {
			t.Fatalf("terminal: %+v", f)
		}
	}
	if _, _, still := h.ActiveTurn("c_room"); still {
		t.Fatal("finished turn still reported")
	}
}

func TestActiveTurnEditKind(t *testing.T) {
	release := make(chan struct{})
	h := newHub(t, stubSender{
		fn: func(context.Context, string, string, string, string, event.TurnEventHandler) (appconversation.SendResult, error) {
			return appconversation.SendResult{UserMessagePersisted: true}, nil
		},
		efn: func(ctx context.Context, _, _, _, _, _ string, _ appjob.SendOptions, _ event.TurnEventHandler) error {
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	})
	ws := dial(t, h)
	sendCommand(t, ws, `{"type":"turn.edit","requestId":"e","conversationId":"c_room","payload":{"messageId":"m_1","content":"rewritten"}}`)
	opened := readFrame(t, ws)
	if opened.Type != "turn.opened" {
		t.Fatalf("opened: %+v", opened)
	}
	id, kind, ok := h.ActiveTurn("c_room")
	if !ok || id != opened.TurnID || kind != "edit" {
		t.Fatalf("active = %q %q %v, want %s edit", id, kind, ok, opened.TurnID)
	}
	close(release)
	if f := readFrame(t, ws); f.Type != "turn.completed" {
		t.Fatalf("terminal: %+v", f)
	}
}

// assertActivity validates one conversation.activity broadcast: the
// envelope names the room and turn, the payload reports the edge and
// the opening command's requestId — and nothing sequenced or
// command-correlated leaks (no seq, no top-level requestId).
func assertActivity(t *testing.T, f frame, state, kind, requestID string, opened frame) {
	t.Helper()
	if f.Type != "conversation.activity" {
		t.Fatalf("activity frame: %+v", f)
	}
	if f.Seq != nil {
		t.Fatalf("broadcast must be unsequenced: %+v", f)
	}
	if f.RequestID != "" {
		t.Fatalf("top-level requestId must stay off the broadcast: %+v", f)
	}
	if f.ConversationID != opened.ConversationID || f.TurnID != opened.TurnID {
		t.Fatalf("activity envelope %+v, want conv=%s turn=%s",
			f, opened.ConversationID, opened.TurnID)
	}
	var p activityPayload
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.State != state || p.Kind != kind || p.RequestID != requestID {
		t.Fatalf("activity payload %+v, want %s/%s/%s", p, state, kind, requestID)
	}
}

// The room-activity notice fans out to every socket on the turn's open
// and terminal edges — never journaled, never seq-numbered — so a tab
// that never opened or watched the room still learns it is live.
func TestActivityBroadcast(t *testing.T) {
	release := make(chan struct{})
	h := newHub(t, stubSender{fn: func(_ context.Context, convID, _, _, _ string,
		onEvent event.TurnEventHandler) (appconversation.SendResult, error) {
		<-release
		onEvent(event.NewDeltaEvent("x"))
		return appconversation.SendResult{ConversationID: convID, UserMessagePersisted: true}, nil
	}})
	owner := dial(t, h)
	observer := dial(t, h)

	sendCommand(t, owner, `{"type":"turn.send","requestId":"req-send","payload":{"content":"hi"}}`)
	opened := readAnyFrame(t, owner)
	if opened.Type != "turn.opened" || opened.Seq == nil || *opened.Seq != 0 {
		t.Fatalf("opened: %+v", opened)
	}
	// The issuing socket hears its own notice right after seq 0 — the
	// payload's requestId is how it tells the echo from foreign work.
	assertActivity(t, readAnyFrame(t, owner), "opened", "send", "req-send", opened)
	// A socket that never touched the command hears the same notice.
	assertActivity(t, readAnyFrame(t, observer), "opened", "send", "req-send", opened)

	// The notice is not journaled: a watcher replaying the whole
	// journal sees turn frames only — a replayed broadcast must never
	// come back.
	late := dial(t, h)
	sendCommand(t, late, fmt.Sprintf(`{"type":"turn.watch","requestId":"w","conversationId":%q,"turnId":%q,"afterSeq":-1}`,
		opened.ConversationID, opened.TurnID))
	f := readAnyFrame(t, late)
	if f.Type != "turn.opened" || f.Seq == nil || *f.Seq != 0 {
		t.Fatalf("replay: %+v", f)
	}
	close(release)
	// The first stream frame after the broadcast still owns seq 1 —
	// the notice consumed no turn sequence.
	f = readAnyFrame(t, late)
	if f.Type != "turn.delta" || f.Seq == nil || *f.Seq != 1 {
		t.Fatalf("post-broadcast seq: %+v", f)
	}
	// The terminal mirrors into a "completed" notice — after the
	// journaled terminal on every socket, watcher or not.
	f = readAnyFrame(t, late)
	if f.Type != "turn.completed" {
		t.Fatalf("terminal: %+v", f)
	}
	assertActivity(t, readAnyFrame(t, late), "completed", "send", "req-send", opened)
	assertActivity(t, readAnyFrame(t, observer), "completed", "send", "req-send", opened)
}
