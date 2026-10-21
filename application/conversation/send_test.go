package appconversation

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	appjob "github.com/jahrulnr/nusallm/application/job"
	"github.com/jahrulnr/nusallm/infrastructure/config"
	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/event"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
	repoconversation "github.com/jahrulnr/nusallm/infrastructure/repository/conversation"
	repogateway "github.com/jahrulnr/nusallm/infrastructure/repository/gateway"
	reposecret "github.com/jahrulnr/nusallm/infrastructure/repository/secret"
	coretext "github.com/jahrulnr/nusallm/infrastructure/service/core/text"
	textcompletion "github.com/jahrulnr/nusallm/infrastructure/service/core/text/completion"
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
		map[enum.Environment]appjob.EnvironmentDriver{enum.EnvironmentLocal: appjob.EnvironmentBundle{
			Services: map[enum.Style]dservice.TextModelService{enum.StyleCompletion: textcompletion.New(coretext.Options{})},
		}},
		config.NewConfig(),
		nil,
		"",
		nil,
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
	result, err := app.Send(t.Context(), "my-room", "gw1", "m1", "ping", nil, appjob.SendOptions{}, nil)
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
	if len(conv.Messages()) != 3 || conv.Last().Content() != "pong" {
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
	if _, err := app.Send(t.Context(), "my-room", "", "", "again", nil, appjob.SendOptions{}, nil); err != nil {
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

// An empty submission — no text and no attachments — is rejected by the
// use case itself with 400, before any room is minted or worker posted.
// Transports share this one decision instead of each gating their own.
func TestSendRejectsEmpty(t *testing.T) {
	app, _ := fixture(t, okHandler)
	for _, msg := range []string{"", "   "} {
		res, err := app.Send(t.Context(), "", "gw1", "m1", msg, nil, appjob.SendOptions{}, nil)
		if apperror.As(err).StatusCode() != apperror.StatusBadRequest {
			t.Fatalf("empty send %q → %v, want 400", msg, err)
		}
		if res.ConversationID != "" || res.UserMessagePersisted {
			t.Fatalf("rejected send must not mint or persist: %+v", res)
		}
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
	if _, err := app.Send(t.Context(), "room", "gw1", "m1", "hi", nil, appjob.SendOptions{}, nil); err != nil {
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
	result, err := app.Send(t.Context(), "", "gw1", "m1", "ping", nil, appjob.SendOptions{}, nil)
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
	result, err := app.Send(t.Context(), "c2", "gw1", "m1", "ping", nil, appjob.SendOptions{}, nil)
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
	last := conv.Last()
	pinOK := last.Role() == enum.RoleSystem && len(last.ToolCalls()) == 1 &&
		last.ToolCalls()[0].Type() == enum.TooltypeInjection
	if len(conv.Messages()) != 2 || !pinOK {
		t.Fatal("failed send must persist user turn + workspace pin, without a reply")
	}
	if conv.Title() != "ping" {
		t.Fatalf("failed first send title = %q, want the persisted user message", conv.Title())
	}
}

// Route/model are resolved eagerly — a dangling route fails the send
// before any message is persisted.
func TestSendUnknownRoute(t *testing.T) {
	app, _ := fixture(t, okHandler)
	_, err := app.Send(t.Context(), "c3", "missing", "m1", "hi", nil, appjob.SendOptions{}, nil)
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
	posted := [2]chan struct{}{make(chan struct{}), make(chan struct{})}
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx := &signaledContext{Context: t.Context(), observed: posted[i]}
			_, _ = app.Send(ctx, "ser", "gw1", "m1", fmt.Sprintf("m%d", i), nil, appjob.SendOptions{}, nil)
		}(i)
	}
	// Both submissions must be posted, then exactly one may be running
	// upstream — goroutine scheduling is not a synchronization point.
	for i := range posted {
		select {
		case <-posted[i]:
		case <-time.After(2 * time.Second):
			t.Fatalf("send %d was never posted", i)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&calls) != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("first send did not reach the upstream: %d calls", calls)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&calls); got != 1 { // second must be queued
		t.Fatalf("sends overlapped: %d upstream calls", got)
	}
	close(release)
	wg.Wait()
	conv, _ := app.deps.Convs.Load(t.Context(), "ser")
	// 2 serialized turns; the second send adds user+answer only — the
	// unchanged workspace pin is not re-recorded.
	if len(conv.Messages()) != 5 {
		t.Fatalf("want 5 messages, got %d", len(conv.Messages()))
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
		result, err := app.Send(t.Context(), "qc", "gw1", "m1", "first", nil, appjob.SendOptions{}, nil)
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
		result, err := app.Send(queuedCtx, "qc", "", "", "second", nil, appjob.SendOptions{}, nil)
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

// A queued send whose caller ctx died before the worker reached it must
// leave no trace: the 408 already told the caller nothing persisted, so a
// job that runs anyway writes a user line contradicting that reply — and
// races whatever cleanup the caller does next (the CI flake this test was
// born from). Cancellation used to arrive only through context.AfterFunc's
// goroutine, which on a busy or single-P machine loses the race against
// the job's own ctx checks; pinning one P keeps that window wide enough
// for the omission to fail reliably.
func TestQueuedSendCanceledBeforeStartPersistsNothing(t *testing.T) {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))

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
		result, err := app.Send(t.Context(), "qc", "gw1", "m1", "first", nil, appjob.SendOptions{}, nil)
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
		result, err := app.Send(queuedCtx, "qc", "", "", "second", nil, appjob.SendOptions{}, nil)
		queuedDone <- sendOutcome{result: result, err: err}
	}()
	<-queuedCtx.observed
	cancel()
	select {
	case outcome := <-queuedDone:
		if apperror.As(outcome.err).StatusCode() != 408 {
			t.Fatalf("queued cancel → %v, want 408", outcome.err)
		}
		if outcome.result.UserMessagePersisted {
			t.Fatal("queued cancellation must report no user line")
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

	// The freed worker now reaches the canceled job; give it every chance
	// to (wrongly) persist before asserting the room kept only the first
	// turn.
	deadline := time.Now().Add(300 * time.Millisecond)
	for {
		messages, err := app.History(t.Context(), "qc", 0, -1)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range messages {
			if m.Role() == enum.RoleUser && m.Content() == "second" {
				t.Fatal("canceled queued send persisted its user line after the 408 reply")
			}
		}
		if time.Now().After(deadline) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Cancel kills the running send — 408, not an upstream error — after its
// user message has committed.
func TestCancelPersistsArrivedDeltasAndCompleteTools(t *testing.T) {
	started := make(chan struct{})
	arrived := make(chan struct{})
	var arrivedOnce sync.Once
	app, gw := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// Tool-call deltas first, the text block last — its DeltaEvent
		// proves every earlier block was already parsed by the client.
		w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"type\":\"function\",\"function\":{\"name\":\"exec\",\"arguments\":\"{\\\"command\\\":\\\"somecode.sh\\\"}\"}}]}}]}\n\n"))
		w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":1,\"id\":\"c2\",\"type\":\"function\",\"function\":{\"name\":\"exec\",\"arguments\":\"{\\\"command\\\":\\\"tail\"}}]}}]}\n\n"))
		w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"think\",\"content\":\"hello\"}}]}\n\n"))
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	})
	gw.EnableStream()
	if err := app.deps.Gateways.Save(t.Context(), gw); err != nil {
		t.Fatal(err)
	}
	done := make(chan sendOutcome, 1)
	go func() {
		result, err := app.Send(t.Context(), "partial", "gw1", "m1", "ping", nil, appjob.SendOptions{}, func(e event.TurnEvent) {
			if e.Kind() == enum.TurnEventDelta && e.Delta() == "hello" {
				arrivedOnce.Do(func() { close(arrived) })
			}
		})
		done <- sendOutcome{result: result, err: err}
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not start")
	}
	// Cancel only once the client consumed every flushed block — the
	// delta on the final block implies the tool-call blocks already ran.
	select {
	case <-arrived:
	case <-time.After(2 * time.Second):
		t.Fatal("streamed delta never arrived")
	}
	if err := app.Cancel(t.Context(), "partial"); err != nil {
		t.Fatal(err)
	}
	select {
	case outcome := <-done:
		if apperror.As(outcome.err).StatusCode() != 408 {
			t.Fatalf("canceled send → %v, want 408", outcome.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not finish the send")
	}
	conv, err := app.deps.Convs.Load(t.Context(), "partial")
	if err != nil {
		t.Fatal(err)
	}
	msgs := conv.Messages()
	if len(msgs) != 3 || msgs[2].Role() != enum.RoleAssistant || msgs[2].Content() != "hello" {
		t.Fatalf("history: %+v", msgs)
	}
	asst := msgs[2]
	var sawReason bool
	for _, part := range asst.Parts() {
		if part.Kind == entity.MessagePartReasoning && part.Text == "think" {
			sawReason = true
		}
	}
	if !sawReason {
		t.Fatalf("reasoning parts: %+v", asst.Parts())
	}
	calls := asst.ToolCalls()
	if len(calls) != 1 || calls[0].Name() != "exec" || calls[0].Input() != `{"command":"somecode.sh"}` {
		t.Fatalf("tools: %+v", calls)
	}
	if !strings.Contains(calls[0].Output(), "interrupted by user") {
		t.Fatalf("output: %s", calls[0].Output())
	}
}

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
		result, err := app.Send(t.Context(), "run", "gw1", "m1", "ping", nil, appjob.SendOptions{}, nil)
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
	// User turn and its workspace record stay; no assistant reply.
	conv, _ := app.deps.Convs.Load(t.Context(), "run")
	last := conv.Last()
	pinOK := last.Role() == enum.RoleSystem && len(last.ToolCalls()) == 1 &&
		last.ToolCalls()[0].Type() == enum.TooltypeInjection
	if len(conv.Messages()) != 2 || !pinOK {
		t.Fatal("canceled send must keep the user turn + workspace pin only")
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
		result, err := app.Send(ctx, "append-race", "gw1", "m1", "ping", nil, appjob.SendOptions{}, nil)
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
	entered := make(chan struct{})
	var enteredOnce sync.Once
	app, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		enteredOnce.Do(func() { close(entered) })
		<-release
		okHandler(w, r)
	})
	running := make(chan error, 1)
	go func() {
		_, err := app.Send(t.Context(), "q", "gw1", "m1", "first", nil, appjob.SendOptions{}, nil)
		running <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first send did not reach the upstream")
	}
	queuedCtx := &signaledContext{Context: t.Context(), observed: make(chan struct{})}
	queued := make(chan error, 1)
	go func() {
		_, err := app.Send(queuedCtx, "q", "", "", "second", nil, appjob.SendOptions{}, nil)
		queued <- err
	}()
	select {
	case <-queuedCtx.observed: // second is queued while the first holds the slot
	case <-time.After(2 * time.Second):
		t.Fatal("second send never queued")
	}

	if err := app.Cancel(t.Context(), "q"); err != nil {
		t.Fatalf("Cancel running: %v", err)
	}
	// The canceled send must fully unwind before the upstream opens: a
	// response landing inside the cancel window is still persisted —
	// partial replies are honest history — and would inflate the count.
	select {
	case err := <-running:
		if apperror.As(err).StatusCode() != 408 {
			t.Fatalf("running send after Cancel = %v, want 408", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Cancel did not reach the running send")
	}
	close(release)
	select {
	case err := <-queued:
		if err != nil {
			t.Fatalf("queued send must proceed after the running one dies: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("queued send never ran after the cancel")
	}
	conv, _ := app.deps.Convs.Load(t.Context(), "q")
	// user1+context, user2+assistant2 — no assistant1, and no second pin:
	// the workspace record already in the tail cites the same directory.
	if len(conv.Messages()) != 4 {
		t.Fatalf("queued turn must land after canceled one: %d msgs", len(conv.Messages()))
	}
}

// A context already dead on arrival must not persist anything.
func TestDeadCtxNoMutation(t *testing.T) {
	app, _ := fixture(t, okHandler)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err := app.Send(ctx, "dead", "gw1", "m1", "ping", nil, appjob.SendOptions{}, nil)
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
		if _, err := app.Send(t.Context(), fmt.Sprintf("evict-%d", i), "gw1", "m1", "ping", nil, appjob.SendOptions{}, nil); err != nil {
			t.Fatal(err)
		}
	}
	waitWorkers(t, app, 0)
}

// A failed send on a fresh room persists the binding — retry with
// route/modelID "" reaches upstream instead of 400. The failure must
// outlast the turn's own bounded retry: a lone 429 is now absorbed
// inside the turn, so the stub fails the whole three-call budget.
func TestFailedFreshSendStaysRetryable(t *testing.T) {
	var calls int32
	app, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) <= 3 {
			w.WriteHeader(429)
			return
		}
		okHandler(w, r)
	})
	if _, err := app.Send(t.Context(), "retry", "gw1", "m1", "ping", nil, appjob.SendOptions{}, nil); err == nil {
		t.Fatal("first send must fail")
	}
	if _, err := app.Send(t.Context(), "retry", "", "", "ping", nil, appjob.SendOptions{}, nil); err != nil {
		t.Fatalf("retry on stored binding: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 4 {
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
		map[enum.Environment]appjob.EnvironmentDriver{enum.EnvironmentLocal: appjob.EnvironmentBundle{
			Services: map[enum.Style]dservice.TextModelService{enum.StyleCompletion: noAppendSvc{}},
		}},
		config.NewConfig(),
		nil,
		"",
		nil,
	)
	result, err := app.Send(t.Context(), "badsvc", "gw1", "m1", "ping", nil, appjob.SendOptions{}, nil)
	if apperror.As(err).StatusCode() != 500 {
		t.Fatalf("zero-append adapter → %v, want 500", err)
	}
	if !result.UserMessagePersisted {
		t.Fatal("adapter failure must still report the persisted user line")
	}
	conv, _ := app.deps.Convs.Load(t.Context(), result.ConversationID)
	if len(conv.Messages()) != 2 {
		t.Fatal("only the user turn and its context record may persist when the adapter adds nothing")
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
	ownerTool := &conversationOwnerTool{}
	app := New(
		convs,
		gateways,
		map[enum.Environment]appjob.EnvironmentDriver{enum.EnvironmentLocal: appjob.EnvironmentBundle{
			Services: map[enum.Style]dservice.TextModelService{enum.StyleCompletion: svc},
			Tools:    map[string]dservice.Tool{"echo": ownerTool},
		}},
		config.NewConfig(),
		nil,
		"",
		nil,
	)
	var evs []event.TurnEvent
	onEvent := func(e event.TurnEvent) { evs = append(evs, e) }
	// The room advertises the echo tool — the turn loop fails closed on
	// calls whose name isn't in the offered catalog.
	seed, _ := aggregate.NewConversation("loop")
	seed.SetTools([]valueobject.ToolSchema{echoTool{}.Schema()})
	if err := convs.Save(t.Context(), seed); err != nil {
		t.Fatal(err)
	}
	result, err := app.Send(t.Context(), "loop", "gw1", "m1", "go", nil, appjob.SendOptions{}, onEvent)
	if err != nil {
		t.Fatal(err)
	}
	if result.ConversationID != "loop" {
		t.Fatalf("convID: %q", result.ConversationID)
	}
	if ownerTool.conversationID != "loop" {
		t.Fatalf("tool output owner = %q, want loop", ownerTool.conversationID)
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
	if len(conv.Messages()) != 4 {
		t.Fatalf("want user+context+toolcall+final, got %d msgs", len(conv.Messages()))
	}
	tc := conv.Messages()[2].ToolCalls()[0]
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

type environmentService struct{ label string }

func (s environmentService) Send(_ context.Context, conv *aggregate.Conversation, _ *aggregate.Gateway, _ entity.Model, _ event.TurnEventHandler) error {
	last := conv.Last()
	calls := last.ToolCalls()
	settled := last.Role() == enum.RoleAssistant && len(calls) > 0 &&
		calls[len(calls)-1].Type() == enum.TooltypeOrigin && calls[len(calls)-1].HasOutput()
	if !settled {
		// User line (or a workspace pin) ends the context → issue a call.
		m, _ := entity.NewMessage(enum.RoleAssistant, "")
		call, _ := entity.NewToolCall("call_1", "echo", `{}`, enum.TooltypeOrigin)
		m.AddToolCall(call)
		m.SetFinishReason(enum.FinishReasonToolCalls)
		conv.Append(m)
		return nil
	}
	m, _ := entity.NewMessage(enum.RoleAssistant, s.label+":"+calls[len(calls)-1].Output())
	m.SetFinishReason(enum.FinishReasonStop)
	conv.Append(m)
	return nil
}

type environmentTool struct {
	echoTool
	label string
}

type virtualWorkspace struct{}

func (virtualWorkspace) CheckRoot(dir string) (string, error) {
	if dir == "/remote/project" {
		return dir, nil
	}
	return "", fmt.Errorf("unknown virtual workspace %q", dir)
}

func (virtualWorkspace) Open(context.Context, string) (drepo.WorkspaceSession, error) {
	return nil, fmt.Errorf("unused by scripted service")
}

func (virtualWorkspace) OpenHost(context.Context, string) (io.ReadCloser, fs.FileInfo, error) {
	return nil, nil, fmt.Errorf("host reads unsupported on virtual workspace")
}

func (virtualWorkspace) HostURI(string) string { return "" }

func (t environmentTool) Execute(context.Context, entity.ToolCall, dservice.ToolEmitter) (dservice.ToolResult, error) {
	return dservice.ToolResult{Text: t.label}, nil
}

func TestSendSelectsEnvironmentPerConversationAndResume(t *testing.T) {
	gw, _ := aggregate.NewGateway("gw1", "stub", "http://unused", enum.StyleCompletion)
	m, _ := entity.NewModel("m1", "gw1")
	gw.SetModels([]entity.Model{m})
	gateways := repogateway.NewGatewayRepository(t.TempDir(), nil)
	if err := gateways.Save(t.Context(), &gw); err != nil {
		t.Fatal(err)
	}
	convs := repoconversation.NewConversationRepository(t.TempDir())
	app := New(convs, gateways, map[enum.Environment]appjob.EnvironmentDriver{
		enum.EnvironmentLocal: appjob.EnvironmentBundle{
			Services: map[enum.Style]dservice.TextModelService{enum.StyleCompletion: environmentService{label: "local"}},
			Tools:    map[string]dservice.Tool{"echo": environmentTool{label: "local"}},
		},
		enum.EnvironmentDocker: appjob.EnvironmentBundle{
			WorkspaceStore: virtualWorkspace{},
			Services:       map[enum.Style]dservice.TextModelService{enum.StyleCompletion: environmentService{label: "docker"}},
			Tools:          map[string]dservice.Tool{"echo": environmentTool{label: "docker"}, appjob.SummaryToolName: appjob.NewSummaryTool()},
		},
	}, config.NewConfig(), nil, "", nil)
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })

	for _, tc := range []struct {
		id   string
		opts appjob.SendOptions
		want string
	}{
		{"local-room", appjob.SendOptions{}, "local:local"},
		{"docker-room", appjob.SendOptions{Environment: enum.EnvironmentDocker, Workspace: "/remote/project"}, "docker:docker"},
		{"docker-room", appjob.SendOptions{}, "docker:docker"},
		{"local-room", appjob.SendOptions{}, "local:local"},
	} {
		if _, err := app.Send(t.Context(), tc.id, "gw1", "m1", "hi", nil, tc.opts, nil); err != nil {
			t.Fatalf("send %s: %v", tc.id, err)
		}
		conv, err := convs.Load(t.Context(), tc.id)
		if err != nil {
			t.Fatal(err)
		}
		if got := conv.Last().Content(); got != tc.want {
			t.Fatalf("%s answer = %q, want %q", tc.id, got, tc.want)
		}
		if tc.id == "docker-room" && conv.Workspace() != "/remote/project" {
			t.Fatalf("docker workspace = %q", conv.Workspace())
		}
	}
	localSchemas, err := app.ToolSchemas(t.Context(), "local-room")
	if err != nil || len(localSchemas) != 1 {
		t.Fatalf("local tool catalog = %v, %v", localSchemas, err)
	}
	dockerSchemas, err := app.ToolSchemas(t.Context(), "docker-room")
	if err != nil || len(dockerSchemas) != 2 || dockerSchemas[1].Name() != "summary" {
		t.Fatalf("docker tool catalog = %v, %v", dockerSchemas, err)
	}
	if _, err := app.Send(t.Context(), "bad-workspace", "gw1", "m1", "hi", nil,
		appjob.SendOptions{Environment: enum.EnvironmentDocker, Workspace: "/unknown"}, nil); apperror.As(err).StatusCode() != 400 {
		t.Fatalf("driver must reject an unknown workspace before persistence: %v", err)
	}
	if _, err := convs.Load(t.Context(), "bad-workspace"); apperror.As(err).StatusCode() != 404 {
		t.Fatalf("invalid workspace persisted a room: %v", err)
	}
	beforeEdit, err := convs.Load(t.Context(), "docker-room")
	if err != nil {
		t.Fatal(err)
	}
	var lastUser entity.Message
	for i := len(beforeEdit.Messages()) - 1; i >= 0; i-- {
		if beforeEdit.Messages()[i].Editable() {
			lastUser = beforeEdit.Messages()[i]
			break
		}
	}
	if err := app.Edit(t.Context(), "docker-room", lastUser.ID(), "edited", nil); err != nil {
		t.Fatal(err)
	}
	afterEdit, err := convs.Load(t.Context(), "docker-room")
	if err != nil {
		t.Fatal(err)
	}
	if afterEdit.Last().Content() != "docker:docker" || afterEdit.Environment() != enum.EnvironmentDocker {
		t.Fatalf("edit used wrong driver: %q %q", afterEdit.Last().Content(), afterEdit.Environment())
	}
	if _, err := app.Send(t.Context(), "docker-room", "", "", "switch", nil,
		appjob.SendOptions{Environment: enum.EnvironmentLocal}, nil); err != nil {
		t.Fatal(err)
	}
	switched, err := convs.Load(t.Context(), "docker-room")
	if err != nil {
		t.Fatal(err)
	}
	if switched.Environment() != enum.EnvironmentLocal || switched.Workspace() != "" || switched.Last().Content() != "local:local" {
		t.Fatalf("switched binding/result = %q %q %q", switched.Environment(), switched.Workspace(), switched.Last().Content())
	}
	if _, err := app.Send(t.Context(), "missing-driver", "gw1", "m1", "hi", nil,
		appjob.SendOptions{Environment: enum.EnvironmentSSH}, nil); apperror.As(err).StatusCode() != 400 {
		t.Fatalf("unregistered driver must fail before writing, got %v", err)
	}
	if _, err := convs.Load(t.Context(), "missing-driver"); apperror.As(err).StatusCode() != 404 {
		t.Fatalf("unregistered driver persisted a room: %v", err)
	}
	orphan, _ := aggregate.NewConversation("unavailable-room")
	orphan.SetRoute("gw1")
	orphan.SetModelID("m1")
	if err := orphan.SetEnvironment(enum.EnvironmentSSH); err != nil {
		t.Fatal(err)
	}
	if err := convs.Save(t.Context(), orphan); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Send(t.Context(), orphan.ID(), "", "", "hi", nil, appjob.SendOptions{}, nil); apperror.As(err).StatusCode() != 400 {
		t.Fatalf("resumed unavailable driver must fail, got %v", err)
	}
	unchanged, err := convs.Load(t.Context(), orphan.ID())
	if err != nil {
		t.Fatal(err)
	}
	if len(unchanged.Messages()) != 0 {
		t.Fatalf("failed resume changed history: messages=%d", len(unchanged.Messages()))
	}
}

type conversationOwnerTool struct {
	echoTool
	conversationID string
}

func (tool *conversationOwnerTool) Execute(ctx context.Context, call entity.ToolCall, emit dservice.ToolEmitter) (dservice.ToolResult, error) {
	tool.conversationID = dservice.ConversationID(ctx)
	return tool.echoTool.Execute(ctx, call, emit)
}

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
