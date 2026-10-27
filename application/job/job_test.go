package job

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"

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
)

// loopSvc asks for a tool on every provider call — the model that never
// stops requesting, which the step cap exists to catch.
type loopSvc struct {
	calls              int
	seenConversationID string
}

func (s *loopSvc) Send(ctx context.Context, conv *aggregate.Conversation, _ *aggregate.Gateway, _ entity.Model, _ event.TurnEventHandler) error {
	s.calls++
	s.seenConversationID = dservice.ConversationID(ctx)
	m, _ := entity.NewMessage(enum.RoleAssistant, "one more tool")
	tc, err := entity.NewToolCall("call_"+string(rune('a'+s.calls)), "echo", `{}`, enum.TooltypeOrigin)
	if err != nil {
		return err
	}
	m.AddToolCall(tc)
	m.SetFinishReason(enum.FinishReasonToolCalls)
	conv.Append(m)
	return nil
}

func TestRunTurnStepCap(t *testing.T) {
	d := Deps{
		SummaryTool: mustSummaryTool(t),
		Convs:       repoconversation.NewConversationRepository(t.TempDir()),
		Config:      config.ConversationConfig{MaxTurnStep: 2},
	}
	conv, _ := aggregate.NewConversation("room")
	user, _ := entity.NewMessage(enum.RoleUser, "hi")
	conv.Append(user)
	if err := d.Convs.Save(t.Context(), conv); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Convs.AppendMessage(t.Context(), "room", user); err != nil {
		t.Fatal(err)
	}
	svc := &loopSvc{}
	err := d.runTurn(t.Context(), "room", conv, EnvironmentBundle{}, nil, entity.Model{}, svc, nil, nil)
	if apperror.As(err).StatusCode() != 500 {
		t.Fatalf("step cap → %v, want 500", err)
	}
	if svc.seenConversationID != "room" {
		t.Fatalf("provider context conversation id = %q, want room", svc.seenConversationID)
	}
}

// partial413Svc commits a message and then fails with a 413 — the
// compact-retry path must not fire when the call already appended: the
// in-memory entry was never persisted.
type partial413Svc struct{ calls int }

func (s *partial413Svc) Send(_ context.Context, conv *aggregate.Conversation, _ *aggregate.Gateway, _ entity.Model, _ event.TurnEventHandler) error {
	s.calls++
	m, _ := entity.NewMessage(enum.RoleAssistant, "partial")
	conv.Append(m)
	return apperror.New(413, "context overflow", errors.New("prompt too long"))
}

func TestRunTurn413WithAppendedMessagePropagates(t *testing.T) {
	d := Deps{
		SummaryTool: mustSummaryTool(t),
		Convs:       repoconversation.NewConversationRepository(t.TempDir()),
		Config:      config.ConversationConfig{MaxTurnStep: 2},
	}
	conv, _ := aggregate.NewConversation("room")
	if err := d.Convs.Save(t.Context(), conv); err != nil {
		t.Fatal(err)
	}
	// Seed a transcript longer than keepTail (4) once the service appends
	// — otherwise the compact-retry branch's length check can never hold
	// and the len(added)==0 guard goes unexercised.
	for _, seed := range []struct {
		role enum.Role
		text string
	}{
		{enum.RoleUser, "hi"},
		{enum.RoleAssistant, "first answer"},
		{enum.RoleUser, "follow-up"},
		{enum.RoleAssistant, "second answer"},
	} {
		m, _ := entity.NewMessage(seed.role, seed.text)
		conv.Append(m)
		if _, err := d.Convs.AppendMessage(t.Context(), "room", m); err != nil {
			t.Fatal(err)
		}
	}
	svc := &partial413Svc{}
	err := d.runTurn(t.Context(), "room", conv, EnvironmentBundle{}, nil, entity.Model{}, svc, nil, nil)
	if apperror.As(err).StatusCode() != 413 {
		t.Fatalf("appended-message 413 must propagate, got %v", err)
	}
	if svc.calls != 1 {
		t.Fatalf("compact-retry fired despite the appended message: calls=%d", svc.calls)
	}
}

// markerSvc only exists to be told apart — resolveRoute's job is picking
// the right adapter, which this compares by identity.
type markerSvc struct{ name string }

func (s *markerSvc) Send(context.Context, *aggregate.Conversation, *aggregate.Gateway, entity.Model, event.TurnEventHandler) error {
	return nil
}

// A model-level style pin outranks the gateway's declared style — one
// mixed-protocol endpoint then serves each family on its own surface.
func TestResolveRouteModelStyleOverride(t *testing.T) {
	gw, err := aggregate.NewGateway("gw1", "stub", "http://unused", enum.StyleCompletion)
	if err != nil {
		t.Fatal(err)
	}
	pinned, _ := entity.NewModel("claude-x", "gw1")
	pinned.SetStyle(enum.StyleMessages)
	plain, _ := entity.NewModel("deepseek-x", "gw1")
	unwired, _ := entity.NewModel("luna-x", "gw1")
	unwired.SetStyle(enum.StyleResponses)
	gw.SetModels([]entity.Model{pinned, plain, unwired})
	gateways := repogateway.NewGatewayRepository(t.TempDir(), nil)
	if err := gateways.Save(t.Context(), &gw); err != nil {
		t.Fatal(err)
	}
	d := Deps{Gateways: gateways}
	completionSvc := &markerSvc{name: "completion"}
	messagesSvc := &markerSvc{name: "messages"}
	driver := EnvironmentBundle{Services: map[enum.Style]dservice.TextModelService{
		enum.StyleCompletion: completionSvc,
		enum.StyleMessages:   messagesSvc,
	}}
	conv, _ := aggregate.NewConversation("room")

	_, _, svc, err := d.resolveRoute(t.Context(), "room", conv, driver, "gw1", "claude-x")
	if err != nil {
		t.Fatal(err)
	}
	if svc != messagesSvc {
		t.Fatalf("pinned model must route to its own adapter, got %v", svc)
	}
	_, _, svc, err = d.resolveRoute(t.Context(), "room", conv, driver, "gw1", "deepseek-x")
	if err != nil {
		t.Fatal(err)
	}
	if svc != completionSvc {
		t.Fatalf("unpinned model must inherit the gateway style, got %v", svc)
	}
	// An override naming a style the deployment never wired is a 500
	// naming the effective style, not the gateway's.
	_, _, _, err = d.resolveRoute(t.Context(), "room", conv, driver, "gw1", "luna-x")
	if apperror.As(err).StatusCode() != 500 {
		t.Fatalf("unwired style → %v, want 500", err)
	}
	if !strings.Contains(apperror.As(err).Caused(), "responses") {
		t.Fatalf("error must name the effective style: %v", err)
	}
}

func TestPendingCallsFilter(t *testing.T) {
	m, _ := entity.NewMessage(enum.RoleAssistant, "calls")
	inject, _ := entity.NewToolCall("c1", "read", `{}`, enum.TooltypeInjection)
	answered, _ := entity.NewToolCall("c2", "echo", `{}`, enum.TooltypeOrigin)
	answered.SetOutput("done")
	pending, _ := entity.NewToolCall("c3", "echo", `{}`, enum.TooltypeOrigin)
	m.AddToolCall(inject)
	m.AddToolCall(answered)
	m.AddToolCall(pending)
	got := pendingCalls(m)
	if len(got) != 1 || got[0].ID() != "c3" {
		t.Fatalf("want only the unanswered origin call, got %+v", got)
	}
}

// quietTool answers without streaming; chattyTool streams progress.
// Together they pin the live-exposure rule: a settled output crosses as
// one late chunk when nothing streamed, and never as a duplicate when
// the tool already emitted. ghost is unregistered — its error result
// must cross too.
type quietTool struct{}

func (quietTool) Schema() valueobject.ToolSchema {
	s, _ := valueobject.NewToolSchema("quiet", "answers without streaming", nil, true, enum.ToolPermissionAgent)
	return s
}

func (quietTool) Execute(context.Context, entity.ToolCall, dservice.ToolEmitter) (dservice.ToolResult, error) {
	return dservice.ToolResult{Text: "settled output"}, nil
}

type chattyTool struct{}

func (chattyTool) Schema() valueobject.ToolSchema {
	s, _ := valueobject.NewToolSchema("chatty", "streams progress", nil, true, enum.ToolPermissionAgent)
	return s
}

func (chattyTool) Execute(_ context.Context, _ entity.ToolCall, emit dservice.ToolEmitter) (dservice.ToolResult, error) {
	emit("progress")
	return dservice.ToolResult{Text: "final answer"}, nil
}

// twoToolSvc requests quiet, chatty, and an unregistered call on the
// first live Send, then answers finally on the second.
type twoToolSvc struct{ calls int }

func (s *twoToolSvc) Send(_ context.Context, conv *aggregate.Conversation, _ *aggregate.Gateway, _ entity.Model, _ event.TurnEventHandler) error {
	s.calls++
	m, _ := entity.NewMessage(enum.RoleAssistant, "working")
	if s.calls == 1 {
		for _, c := range [][2]string{{"c1", "quiet"}, {"c2", "chatty"}, {"c3", "ghost"}} {
			tc, err := entity.NewToolCall(c[0], c[1], `{}`, enum.TooltypeOrigin)
			if err != nil {
				return err
			}
			m.AddToolCall(tc)
		}
		m.SetFinishReason(enum.FinishReasonToolCalls)
	}
	conv.Append(m)
	return nil
}

func TestToolOutputExposedLive(t *testing.T) {
	d := Deps{
		SummaryTool: mustSummaryTool(t),
		Convs:       repoconversation.NewConversationRepository(t.TempDir()),
		Config:      config.ConversationConfig{MaxTurnStep: 4},
	}
	conv, _ := aggregate.NewConversation("room")
	user, _ := entity.NewMessage(enum.RoleUser, "hi")
	conv.Append(user)
	if err := d.Convs.Save(t.Context(), conv); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Convs.AppendMessage(t.Context(), "room", user); err != nil {
		t.Fatal(err)
	}
	svc := &twoToolSvc{}
	driver := EnvironmentBundle{Tools: map[string]dservice.Tool{
		"quiet":  quietTool{},
		"chatty": chattyTool{},
	}}
	var chunks []string
	onEvent := func(ev event.TurnEvent) {
		if ev.Kind() == enum.TurnEventToolOutput {
			chunks = append(chunks, ev.ToolCall().ID()+":"+ev.Delta())
		}
	}
	if err := d.runTurn(t.Context(), "room", conv, driver, nil, entity.Model{}, svc, onEvent, nil); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"c1:settled output",
		"c2:progress",
		"c3:error: tool not permitted",
	}
	if len(chunks) != len(want) {
		t.Fatalf("tool_output chunks = %v, want %v", chunks, want)
	}
	for i := range want {
		if chunks[i] != want[i] {
			t.Fatalf("chunk %d = %q, want %q (all: %v)", i, chunks[i], want[i], chunks)
		}
	}
}

func TestInterruptBeforeExecute(t *testing.T) {
	var b Base
	b.Interrupt()
	ctx := b.Cancellable(context.Background(), t.Context())
	if ctx.Err() != context.Canceled {
		t.Fatal("interrupt before execute must leave the ctx canceled")
	}
}

func TestReplyNilResultIsNoop(t *testing.T) {
	var b Base
	b.Reply(nil) // must not block — a hand-built job has no waiter
}

func TestFinishPanicBecomes500(t *testing.T) {
	b := &Base{Result: make(chan error, 1)}
	err := func() (err error) {
		defer b.finish(&err)
		panic("boom")
	}()
	if apperror.As(err).StatusCode() != 500 {
		t.Fatalf("panic → %v, want 500", err)
	}
	if got := <-b.Result; apperror.As(got).StatusCode() != 500 {
		t.Fatalf("reply → %v, want 500", got)
	}
}

// Cancellable hands the job a ctx that dies with the caller's ctx. A caller
// already gone at start yields a canceled ctx immediately (the direct check).
// A cancel that lands mid-Execute rides context.AfterFunc's own goroutine, so
// the instant it reaches the derived ctx is scheduler-dependent. The test
// asserts only that the cancel eventually lands.
func TestCancellableCallerCancel(t *testing.T) {
	var gone Base
	dead, cancelDead := context.WithCancel(context.Background())
	cancelDead()
	if err := gone.Cancellable(context.Background(), dead).Err(); err == nil {
		t.Fatal("caller gone before start must leave the job ctx canceled")
	}

	var b Base
	caller, cancel := context.WithCancel(context.Background())
	ctx := b.Cancellable(context.Background(), caller)
	cancel()
	deadline := time.Now().Add(2 * time.Second)
	for ctx.Err() == nil {
		if time.Now().After(deadline) {
			t.Fatal("AfterFunc never propagated caller cancel")
		}
		runtime.Gosched()
	}
}

// honoringConvs wraps the real store with the behavior most ctx-aware
// ports have: a write on a dead context fails. The JSONL repository
// ignores ctx entirely — which is what lets runTurn's cancel-path
// cleanup (AppendMessage/Save/UpdateToolCallOutput calls reusing the
// already-dead turn ctx) land today. This wrapper makes the latent
// dependency visible.
type honoringConvs struct{ drepo.Conversation }

func (c honoringConvs) Save(ctx context.Context, conv *aggregate.Conversation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.Conversation.Save(ctx, conv)
}
func (c honoringConvs) AppendMessage(ctx context.Context, convID string, msg entity.Message) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return c.Conversation.AppendMessage(ctx, convID, msg)
}
func (c honoringConvs) UpdateToolCallOutput(ctx context.Context, convID string, call entity.ToolCall) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.Conversation.UpdateToolCallOutput(ctx, convID, call)
}

// cancelingTool cancels the turn ctx mid-execution, then reports failure —
// the shape of an interrupt landing inside a running tool.
type cancelingTool struct {
	schema valueobject.ToolSchema
	do     context.CancelFunc
}

func (t cancelingTool) Schema() valueobject.ToolSchema { return t.schema }
func (t cancelingTool) Execute(ctx context.Context, _ entity.ToolCall, _ dservice.ToolEmitter) (dservice.ToolResult, error) {
	t.do()
	return dservice.ToolResult{}, ctx.Err()
}

// twoPendingSvc leaves two unanswered origin calls on the record — the
// first executes (and cancels), the second never runs.
type twoPendingSvc struct{ calls int }

func (s *twoPendingSvc) Send(_ context.Context, conv *aggregate.Conversation, _ *aggregate.Gateway, _ entity.Model, _ event.TurnEventHandler) error {
	s.calls++
	m, _ := entity.NewMessage(enum.RoleAssistant, "dispatching")
	if s.calls == 1 {
		for _, c := range [][2]string{{"t1", "push"}, {"t2", "push"}} {
			tc, err := entity.NewToolCall(c[0], c[1], `{}`, enum.TooltypeOrigin)
			if err != nil {
				return err
			}
			m.AddToolCall(tc)
		}
		m.SetFinishReason(enum.FinishReasonToolCalls)
	}
	conv.Append(m)
	return nil
}

// The turn's cancel path persists "interrupted by user" markers and the
// partial reply on the same ctx that just died. It works today only
// because the repository ignores ctx; against a ctx-honoring store the
// markers never land — orphaned pending calls stay in history forever —
// and the raw ctx error escapes as a 500 instead of the 408 the caller
// expects for a canceled turn.
func TestRunTurnCancelCleanupUsesDeadCtx(t *testing.T) {
	repo := repoconversation.NewConversationRepository(t.TempDir())
	d := Deps{
		SummaryTool: mustSummaryTool(t),
		Convs:       honoringConvs{repo},
		Config:      config.ConversationConfig{MaxTurnStep: 4},
	}
	conv, _ := aggregate.NewConversation("room")
	user, _ := entity.NewMessage(enum.RoleUser, "hi")
	conv.Append(user)
	if err := repo.Save(t.Context(), conv); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.AppendMessage(t.Context(), "room", user); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	schema, _ := valueobject.NewToolSchema("push", "test tool", nil, true, enum.ToolPermissionAgent)
	driver := EnvironmentBundle{Tools: map[string]dservice.Tool{
		"push": cancelingTool{schema: schema, do: cancel},
	}}
	svc := &twoPendingSvc{}
	runErr := d.runTurn(ctx, "room", conv, driver, nil, entity.Model{}, svc, nil, nil)
	stored, err := repo.Load(context.Background(), "room")
	if err != nil {
		t.Fatal(err)
	}
	msgs := stored.Messages()
	last := msgs[len(msgs)-1]
	for _, tc := range last.ToolCalls() {
		if !tc.HasOutput() {
			t.Errorf("call %s left pending on disk — interrupted markers never persisted: %+v", tc.ID(), last.ToolCalls())
		}
	}
	if code := apperror.As(runErr).StatusCode(); code != 408 {
		t.Errorf("cancel during tool exec → %v (status %d), want 408 — the interrupted-marker write failed on the dead ctx and its raw error escaped", runErr, code)
	}
}

// SteeringReceipt is exported and zero-value-constructible — a caller can
// hand &SteeringReceipt{} to SteeringInput.Recall, which reads State() on
// it. State()'s unchecked atomic.Value assertion must not panic on the
// caller's goroutine (outside the worker's panic guard).
func TestSteeringReceiptZeroValueState(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("State() on a zero-value receipt panicked: %v", r)
		}
	}()
	if got := (&SteeringReceipt{}).State(); got != SteeringPending {
		t.Fatalf("zero-value receipt state = %q, want pending-or-benign", got)
	}
	in := NewSteeringInput()
	if (&SteeringInput{}).Recall(&SteeringReceipt{}) {
		t.Fatal("recall of a zero-value receipt must not succeed")
	}
	_ = in
}

// A registered-but-nil driver and an unregistered one must both fail with
// the same explicit 400 — never reach a nil interface method call.
func TestEnvironmentRejectsNilAndUnregisteredDrivers(t *testing.T) {
	conv, err := aggregate.NewConversation("room")
	if err != nil {
		t.Fatal(err)
	}
	d := Deps{
		SummaryTool: mustSummaryTool(t), Environments: map[enum.Environment]EnvironmentDriver{enum.EnvironmentLocal: nil}}
	if _, err := d.environment(conv); apperror.As(err).StatusCode() != 400 {
		t.Fatalf("nil driver => %v, want 400", err)
	}
	d = Deps{Environments: map[enum.Environment]EnvironmentDriver{}}
	if _, err := d.environment(conv); apperror.As(err).StatusCode() != 400 {
		t.Fatalf("unregistered driver => %v, want 400", err)
	}
}
