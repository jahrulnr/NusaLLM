package appconversation

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jahrulnr/llm-base/infrastructure/config"
	"github.com/jahrulnr/llm-base/infrastructure/domain/aggregate"
	"github.com/jahrulnr/llm-base/infrastructure/domain/entity"
	"github.com/jahrulnr/llm-base/infrastructure/domain/enum"
	"github.com/jahrulnr/llm-base/infrastructure/domain/event"
	drepo "github.com/jahrulnr/llm-base/infrastructure/domain/repository"
	dservice "github.com/jahrulnr/llm-base/infrastructure/domain/service"
	"github.com/jahrulnr/llm-base/infrastructure/domain/valueobject"
	apperror "github.com/jahrulnr/llm-base/infrastructure/error"
	repoconversation "github.com/jahrulnr/llm-base/infrastructure/repository/conversation"
	repogateway "github.com/jahrulnr/llm-base/infrastructure/repository/gateway"
	reposecret "github.com/jahrulnr/llm-base/infrastructure/repository/secret"
	textcompletion "github.com/jahrulnr/llm-base/infrastructure/service/core/text/completion"
)

type sendOutcome struct {
	result SendResult
	err    error
}

type signaledContext struct {
	context.Context
	observed chan struct{}
	once     sync.Once
}

func (ctx *signaledContext) Done() <-chan struct{} {
	ctx.once.Do(func() { close(ctx.observed) })
	return ctx.Context.Done()
}

type appendGateRepo struct {
	drepo.Conversation
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *appendGateRepo) AppendMessage(ctx context.Context, convID string, msg entity.Message) (bool, error) {
	if msg.Role() == enum.RoleUser {
		r.once.Do(func() { close(r.entered) })
		<-r.release
	}
	return r.Conversation.AppendMessage(ctx, convID, msg)
}

// fixture wires real JSONL repos + a stub completion upstream.
func fixture(t *testing.T, handler http.HandlerFunc) (*ConversationApp, *aggregate.Gateway) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	gw, err := aggregate.NewGateway("gw1", "stub", srv.URL, enum.StyleCompletion)
	if err != nil {
		t.Fatal(err)
	}
	gw.SetApiKey("sk-test")
	gw.DisableStream() // stub serves unary responses
	m, _ := entity.NewModel("m1", "gw1")
	gw.SetModels([]entity.Model{m})

	secrets, err := reposecret.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { secrets.Close() })
	gateways := repogateway.NewGatewayRepository(t.TempDir(), secrets)
	if err := gateways.Save(t.Context(), &gw); err != nil {
		t.Fatal(err)
	}
	app := New(
		repoconversation.NewConversationRepository(t.TempDir()),
		gateways,
		map[enum.Style]dservice.TextModelService{enum.StyleCompletion: textcompletion.New(textcompletion.Options{})},
		nil, // no tool executors
		config.NewConfig(),
	)
	return app, &gw
}

func okHandler(w http.ResponseWriter, r *http.Request) {
	if err := json.NewEncoder(w).Encode(map[string]any{
		"choices": []any{map[string]any{
			"message":       map[string]any{"role": "assistant", "content": "pong"},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
	}); err != nil {
		http.Error(w, "failed to encode fixture response", http.StatusInternalServerError)
	}
}

func TestSendRoundtrip(t *testing.T) {
	app, _ := fixture(t, okHandler)

	// FE-minted convID: send upserts the room and echoes the id back.
	result, err := app.Send(t.Context(), "my-room", "gw1", "m1", "ping", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.ConversationID != "my-room" {
		t.Fatalf("convID not echoed: %q", result.ConversationID)
	}
	if !result.UserMessagePersisted {
		t.Fatal("successful send must report its user message as persisted")
	}
	conv, err := app.deps.Convs.Load(t.Context(), "my-room")
	if err != nil {
		t.Fatal(err)
	}
	if len(conv.Messages()) != 2 || conv.Last().Content() != "pong" {
		t.Fatalf("turns: %+v", conv.Messages())
	}
	if conv.Route() != "gw1" || conv.ModelID() != "m1" || conv.Usage().Total() != 15 {
		t.Fatalf("binding/usage not persisted: %s/%s %+v", conv.Route(), conv.ModelID(), conv.Usage())
	}
	if conv.Title() != "ping" {
		t.Fatalf("first user message title = %q, want ping", conv.Title())
	}
	if conv.SystemPrompt() != "" {
		t.Fatal("the embedded prompt must never persist — it is applied in memory only")
	}
	if conv.Messages()[0].ID() == "" || conv.Last().ID() == "" {
		t.Fatal("message ids missing")
	}

	// Second send on stored bindings — empty route/modelID reuse them and
	// must not replace the original title.
	if _, err := app.Send(t.Context(), "my-room", "", "", "again", nil, nil); err != nil {
		t.Fatal(err)
	}
	conv, err = app.deps.Convs.Load(t.Context(), "my-room")
	if err != nil {
		t.Fatal(err)
	}
	if conv.Title() != "ping" {
		t.Fatalf("later user message replaced first-turn title: %q", conv.Title())
	}
}

// The prompt is code: the wire must see a system message while nothing
// about it lands on disk.
func TestSendAppliesPromptWithoutPersisting(t *testing.T) {
	var sawSystem atomic.Bool
	handler := func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		for _, m := range req.Messages {
			if m.Role == "system" {
				sawSystem.Store(true)
			}
		}
		okHandler(w, r)
	}
	app, _ := fixture(t, handler)
	if _, err := app.Send(t.Context(), "room", "gw1", "m1", "hi", nil, nil); err != nil {
		t.Fatal(err)
	}
	if !sawSystem.Load() {
		t.Fatal("embedded system prompt must reach the wire")
	}
	conv, err := app.deps.Convs.Load(t.Context(), "room")
	if err != nil {
		t.Fatal(err)
	}
	if conv.SystemPrompt() != "" {
		t.Fatal("prompt must not persist in meta")
	}
}

// convID "" means "new room" — the backend mints and returns the id, so
// the caller always learns which room received the message.
func TestSendMintsConvID(t *testing.T) {
	app, _ := fixture(t, okHandler)
	result, err := app.Send(t.Context(), "", "gw1", "m1", "ping", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.ConversationID == "" {
		t.Fatal("no convID returned for a minted room")
	}
	if !result.UserMessagePersisted {
		t.Fatal("successful send must report its user message as persisted")
	}
	if _, err := app.deps.Convs.Load(t.Context(), result.ConversationID); err != nil {
		t.Fatalf("minted room not persisted: %v", err)
	}
}

func TestSendFailedKeepsUserTurn(t *testing.T) {
	app, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		if _, err := w.Write([]byte(`{"error":{"message":"slow down"}}`)); err != nil {
			t.Errorf("write provider error: %v", err)
		}
	})
	result, err := app.Send(t.Context(), "c2", "gw1", "m1", "ping", nil, nil)
	if apperror.As(err).StatusCode() != 429 {
		t.Fatalf("want 429, got %v", err)
	}
	if result.ConversationID != "c2" {
		t.Fatal("even a failed send must name its room")
	}
	if !result.UserMessagePersisted {
		t.Fatal("failed provider send must report its committed user line")
	}
	conv, _ := app.deps.Convs.Load(t.Context(), "c2")
	if len(conv.Messages()) != 1 || conv.Last().Role() != enum.RoleUser {
		t.Fatal("failed send must persist user turn without an assistant reply")
	}
	if conv.Title() != "ping" {
		t.Fatalf("failed first send title = %q, want the persisted user message", conv.Title())
	}
}

// Route/model are resolved eagerly — a dangling route fails the send
// before any message is persisted.
func TestSendUnknownRoute(t *testing.T) {
	app, _ := fixture(t, okHandler)
	_, err := app.Send(t.Context(), "c3", "missing", "m1", "hi", nil, nil)
	if apperror.As(err).StatusCode() != 404 {
		t.Fatalf("dangling route → %v, want 404", err)
	}
	// Nothing should be persisted — the room is never even created.
	if _, err := app.deps.Convs.Load(t.Context(), "c3"); apperror.As(err).StatusCode() != 404 {
		t.Fatal("failed route resolution must not create or persist a turn")
	}
}

// Two sends on one conversation must serialize — queued, not interleaved.
func TestSendSerializesPerConv(t *testing.T) {
	release := make(chan struct{})
	var calls int32
	app, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		<-release // hold each response until released
		okHandler(w, r)
	})
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = app.Send(t.Context(), "ser", "gw1", "m1", fmt.Sprintf("m%d", i), nil, nil)
		}(i)
	}
	time.Sleep(100 * time.Millisecond)             // both queued/running
	if got := atomic.LoadInt32(&calls); got != 1 { // second must be queued
		t.Fatalf("sends overlapped: %d upstream calls", got)
	}
	close(release)
	wg.Wait()
	conv, _ := app.deps.Convs.Load(t.Context(), "ser")
	if len(conv.Messages()) != 4 { // 2 serialized pairs
		t.Fatalf("want 4 turns, got %d", len(conv.Messages()))
	}
}

// A queued send abandoned by its caller must return 408 without claiming
// that its own user line was persisted.
func TestQueuedSendCancelable(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	entered := make(chan struct{})
	var enteredOnce sync.Once
	app, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		enteredOnce.Do(func() { close(entered) })
		<-release
		okHandler(w, r)
	})
	firstDone := make(chan sendOutcome, 1)
	go func() {
		result, err := app.Send(t.Context(), "qc", "gw1", "m1", "first", nil, nil)
		firstDone <- sendOutcome{result: result, err: err}
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first send did not reach the upstream")
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	queuedCtx := &signaledContext{Context: ctx, observed: make(chan struct{})}
	queuedDone := make(chan sendOutcome, 1)
	go func() {
		result, err := app.Send(queuedCtx, "qc", "", "", "second", nil, nil)
		queuedDone <- sendOutcome{result: result, err: err}
	}()
	<-queuedCtx.observed // Send calls Done only after the worker accepted the queued job.
	cancel()
	select {
	case outcome := <-queuedDone:
		if apperror.As(outcome.err).StatusCode() != 408 {
			t.Fatalf("queued cancel → %v, want 408", outcome.err)
		}
		if outcome.result.UserMessagePersisted {
			t.Fatal("queued cancellation must report no user line for this submission")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("queued send not cancelable")
	}
	unblock()
	select {
	case outcome := <-firstDone:
		if outcome.err != nil {
			t.Fatalf("first send: %v", outcome.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first send did not finish after release")
	}
}

// Cancel kills the running send — 408, not an upstream error — after its
// user message has committed.
func TestCancelRunningSend(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	entered := make(chan struct{})
	app, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		okHandler(w, r)
	})
	done := make(chan sendOutcome, 1)
	go func() {
		result, err := app.Send(t.Context(), "run", "gw1", "m1", "ping", nil, nil)
		done <- sendOutcome{result: result, err: err}
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("send did not reach the upstream")
	}

	if err := app.Cancel(t.Context(), "run"); err != nil {
		t.Fatalf("Cancel on a running send: %v", err)
	}
	select {
	case outcome := <-done:
		if apperror.As(outcome.err).StatusCode() != 408 {
			t.Fatalf("canceled send → %v, want 408", outcome.err)
		}
		if !outcome.result.UserMessagePersisted {
			t.Fatal("running cancellation after AppendMessage must report the committed user line")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Cancel did not reach the running send")
	}
	// User turn stays; no assistant reply.
	conv, _ := app.deps.Convs.Load(t.Context(), "run")
	if len(conv.Messages()) != 1 || conv.Last().Role() != enum.RoleUser {
		t.Fatal("canceled send must keep the user turn only")
	}
}

// When cancellation races the repository append, Send waits for the
// append receipt so its result reflects the line that actually committed.
func TestSendCancelRacingAppendReportsPersisted(t *testing.T) {
	app, _ := fixture(t, okHandler)
	gate := &appendGateRepo{
		Conversation: app.deps.Convs,
		entered:      make(chan struct{}),
		release:      make(chan struct{}),
	}
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(gate.release) }) }
	defer unblock()
	app.deps.Convs = gate
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan sendOutcome, 1)
	go func() {
		result, err := app.Send(ctx, "append-race", "gw1", "m1", "ping", nil, nil)
		done <- sendOutcome{result: result, err: err}
	}()
	select {
	case <-gate.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("user AppendMessage did not start")
	}

	cancel()
	select {
	case outcome := <-done:
		t.Fatalf("Send returned before the in-flight append settled: %+v, %v", outcome.result, outcome.err)
	case <-time.After(50 * time.Millisecond):
	}
	unblock()
	select {
	case outcome := <-done:
		if apperror.As(outcome.err).StatusCode() != 408 {
			t.Fatalf("append-racing cancel → %v, want 408", outcome.err)
		}
		if !outcome.result.UserMessagePersisted {
			t.Fatal("successful in-flight append must be reported as persisted")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Send did not return after the append settled")
	}
	conv, err := app.deps.Convs.Load(t.Context(), "append-race")
	if err != nil {
		t.Fatal(err)
	}
	if len(conv.Messages()) != 1 || conv.Messages()[0].Role() != enum.RoleUser {
		t.Fatalf("committed append missing from history: %+v", conv.Messages())
	}
}

// Cancel on an idle conversation is an honest 404 — and the worker
// spawned to answer it evaporates instead of leaking.
func TestCancelIdle(t *testing.T) {
	app, _ := fixture(t, okHandler)
	if err := app.Cancel(t.Context(), "nobody"); apperror.As(err).StatusCode() != 404 {
		t.Fatalf("idle Cancel → %v, want 404", err)
	}
	waitWorkers(t, app, 0)
}

// waitWorkers polls until the workers map reaches n — worker exit is
// asynchronous, so a bare assert would race it.
func waitWorkers(t *testing.T, app *ConversationApp, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		app.mu.Lock()
		got := len(app.workers)
		app.mu.Unlock()
		if got == n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("workers map stuck at %d, want %d", len(app.workers), n)
}

// Cancel targets the RUNNING send — a queued send is untouched and
// proceeds once the slot frees.
func TestCancelLeavesQueuedSend(t *testing.T) {
	release := make(chan struct{})
	app, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		<-release
		okHandler(w, r)
	})
	running := make(chan error, 1)
	go func() {
		_, err := app.Send(t.Context(), "q", "gw1", "m1", "first", nil, nil)
		running <- err
	}()
	time.Sleep(50 * time.Millisecond)
	queued := make(chan error, 1)
	go func() { _, err := app.Send(t.Context(), "q", "", "", "second", nil, nil); queued <- err }()
	time.Sleep(50 * time.Millisecond) // second queued

	if err := app.Cancel(t.Context(), "q"); err != nil {
		t.Fatalf("Cancel running: %v", err)
	}
	close(release)
	if err := <-running; apperror.As(err).StatusCode() != 408 {
		t.Fatalf("running send after Cancel = %v, want 408", err)
	}
	if err := <-queued; err != nil {
		t.Fatalf("queued send must proceed after the running one dies: %v", err)
	}
	conv, _ := app.deps.Convs.Load(t.Context(), "q")
	if len(conv.Messages()) != 3 { // user1 + user2 + assistant2 — no assistant1
		t.Fatalf("queued turn must land after canceled one: %d msgs", len(conv.Messages()))
	}
}

// A context already dead on arrival must not persist anything.
func TestDeadCtxNoMutation(t *testing.T) {
	app, _ := fixture(t, okHandler)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err := app.Send(ctx, "dead", "gw1", "m1", "ping", nil, nil)
	if apperror.As(err).StatusCode() != 408 {
		t.Fatalf("dead ctx → %v, want 408", err)
	}
	if result.UserMessagePersisted {
		t.Fatal("pre-canceled send must not report a persisted user line")
	}
	if _, err := app.deps.Convs.Load(t.Context(), "dead"); apperror.As(err).StatusCode() != 404 {
		t.Fatal("dead ctx send must not create a room or persist a turn")
	}
	waitWorkers(t, app, 0) // the spawn-for-nothing worker must evaporate
}

// Lock entries evaporate once nobody holds or waits on them — FE-minted
// ids must not grow the map forever.
func TestLockEvictedAfterSend(t *testing.T) {
	app, _ := fixture(t, okHandler)
	for i := 0; i < 3; i++ {
		if _, err := app.Send(t.Context(), fmt.Sprintf("evict-%d", i), "gw1", "m1", "ping", nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	waitWorkers(t, app, 0)
}

// A failed send on a fresh room persists the binding — retry with
// route/modelID "" reaches upstream instead of 400.
func TestFailedFreshSendStaysRetryable(t *testing.T) {
	var calls int32
	app, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(429)
			return
		}
		okHandler(w, r)
	})
	if _, err := app.Send(t.Context(), "retry", "gw1", "m1", "ping", nil, nil); err == nil {
		t.Fatal("first send must fail")
	}
	if _, err := app.Send(t.Context(), "retry", "", "", "ping", nil, nil); err != nil {
		t.Fatalf("retry on stored binding: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("retry never reached upstream: %d calls", got)
	}
}

// An adapter that returns nil but appends nothing violates the port —
// the app must surface it as 500, not persist stale state.
func TestAdapterNoAppendIsInternalError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(okHandler))
	defer srv.Close()
	gw, _ := aggregate.NewGateway("gw1", "stub", srv.URL, enum.StyleCompletion)
	gw.DisableStream()
	m, _ := entity.NewModel("m1", "gw1")
	gw.SetModels([]entity.Model{m})
	gateways := repogateway.NewGatewayRepository(t.TempDir(), nil)
	if err := gateways.Save(t.Context(), &gw); err != nil {
		t.Fatal(err)
	}
	app := New(
		repoconversation.NewConversationRepository(t.TempDir()),
		gateways,
		map[enum.Style]dservice.TextModelService{enum.StyleCompletion: noAppendSvc{}},
		nil,
		config.NewConfig(),
	)
	result, err := app.Send(t.Context(), "badsvc", "gw1", "m1", "ping", nil, nil)
	if apperror.As(err).StatusCode() != 500 {
		t.Fatalf("zero-append adapter → %v, want 500", err)
	}
	if !result.UserMessagePersisted {
		t.Fatal("adapter failure must still report the persisted user line")
	}
	conv, _ := app.deps.Convs.Load(t.Context(), result.ConversationID)
	if len(conv.Messages()) != 1 {
		t.Fatal("only the user turn may persist when the adapter adds nothing")
	}
}

type noAppendSvc struct{}

func (noAppendSvc) Send(context.Context, *aggregate.Conversation, *aggregate.Gateway, entity.Model, event.TurnEventHandler) error {
	return nil
}

// A model asking for tools drives the loop: the assistant message persists
// with a pending call, the executor's output lands via a patch, and the
// second provider call ends the turn.
func TestSendToolLoop(t *testing.T) {
	gw, _ := aggregate.NewGateway("gw1", "stub", "http://unused", enum.StyleCompletion)
	gw.DisableStream()
	m, _ := entity.NewModel("m1", "gw1")
	gw.SetModels([]entity.Model{m})
	gateways := repogateway.NewGatewayRepository(t.TempDir(), nil)
	if err := gateways.Save(t.Context(), &gw); err != nil {
		t.Fatal(err)
	}
	svc := &scriptSvc{}
	convs := repoconversation.NewConversationRepository(t.TempDir())
	app := New(
		convs,
		gateways,
		map[enum.Style]dservice.TextModelService{enum.StyleCompletion: svc},
		map[string]dservice.Tool{"echo": echoTool{}},
		config.NewConfig(),
	)
	var evs []event.TurnEvent
	onEvent := func(e event.TurnEvent) { evs = append(evs, e) }
	// The room advertises the echo tool — execTool fails closed on calls
	// whose name isn't in the offered catalog.
	seed, _ := aggregate.NewConversation("loop")
	seed.SetTools([]valueobject.ToolSchema{echoTool{}.Schema()})
	if err := convs.Save(t.Context(), seed); err != nil {
		t.Fatal(err)
	}
	result, err := app.Send(t.Context(), "loop", "gw1", "m1", "go", nil, onEvent)
	if err != nil {
		t.Fatal(err)
	}
	if result.ConversationID != "loop" {
		t.Fatalf("convID: %q", result.ConversationID)
	}
	if !result.UserMessagePersisted {
		t.Fatal("successful send must report its user message as persisted")
	}
	// The executor's chunk must reach the handler scoped to the call id.
	var chunk bool
	for _, e := range evs {
		if e.Kind() == enum.TurnEventToolOutput &&
			e.ToolCall().ID() == "call_1" && e.Delta() == "running…" {
			chunk = true
		}
	}
	if !chunk {
		t.Fatal("tool_output event missing — live chunks didn't stream")
	}
	if atomic.LoadInt32(&svc.calls) != 2 {
		t.Fatalf("expected 2 provider calls, got %d", svc.calls)
	}
	conv, err := app.deps.Convs.Load(t.Context(), "loop")
	if err != nil {
		t.Fatal(err)
	}
	if len(conv.Messages()) != 3 {
		t.Fatalf("want user+toolcall+final, got %d msgs", len(conv.Messages()))
	}
	tc := conv.Messages()[1].ToolCalls()[0]
	if tc.Output() == "" || !strings.Contains(tc.Output(), "echoed:") {
		t.Fatalf("tool output not patched through: %q", tc.Output())
	}
	if conv.Last().Content() != "done" {
		t.Fatalf("final answer: %q", conv.Last().Content())
	}
}

// scriptSvc scripts the turn loop: first call demands a tool, the second
// answers with finish=stop. It satisfies the TextModelService port.
type scriptSvc struct{ calls int32 }

func (s *scriptSvc) Send(_ context.Context, conv *aggregate.Conversation, _ *aggregate.Gateway, _ entity.Model, _ event.TurnEventHandler) error {
	n := atomic.AddInt32(&s.calls, 1)
	var m entity.Message
	if n == 1 {
		m, _ = entity.NewMessage(enum.RoleAssistant, "need a tool")
		tc, err := entity.NewToolCall("call_1", "echo", `{"text":"hi"}`, enum.TooltypeOrigin)
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

type echoTool struct{}

func (echoTool) Schema() valueobject.ToolSchema {
	s, _ := valueobject.NewToolSchema("echo", "echo input", nil, true, enum.ToolPermissionAgent)
	return s
}

func (echoTool) Execute(_ context.Context, call entity.ToolCall, emit dservice.ToolEmitter) (dservice.ToolResult, error) {
	if emit != nil {
		emit("running…")
	}
	return dservice.ToolResult{Text: "echoed: " + call.Input()}, nil
}
