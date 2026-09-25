package appconversation

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jahrulnr/llm-base/infrastructure/config"
	"github.com/jahrulnr/llm-base/infrastructure/domain/aggregate"
	"github.com/jahrulnr/llm-base/infrastructure/domain/entity"
	"github.com/jahrulnr/llm-base/infrastructure/domain/enum"
	"github.com/jahrulnr/llm-base/infrastructure/domain/event"
	drepo "github.com/jahrulnr/llm-base/infrastructure/domain/repository"
	dservice "github.com/jahrulnr/llm-base/infrastructure/domain/service"
	apperror "github.com/jahrulnr/llm-base/infrastructure/error"
	repoconversation "github.com/jahrulnr/llm-base/infrastructure/repository/conversation"
	repogateway "github.com/jahrulnr/llm-base/infrastructure/repository/gateway"
	reposecret "github.com/jahrulnr/llm-base/infrastructure/repository/secret"
	toolsummary "github.com/jahrulnr/llm-base/infrastructure/service/tool/summary"
)

// compactSvc answers the scratch dialogue (the conv carrying the summary
// tool) with the checkpoint call, and any other conversation normally.
type compactSvc struct{ calls atomic.Int32 }

func (s *compactSvc) Send(_ context.Context, conv *aggregate.Conversation, _ *aggregate.Gateway, _ entity.Model, _ event.TurnEventHandler) error {
	s.calls.Add(1)
	for _, t := range conv.Tools() {
		if t.Name() == toolsummary.Name {
			m, _ := entity.NewMessage(enum.RoleAssistant, "")
			tc, err := entity.NewToolCall("sum_1", toolsummary.Name, `{"text":"CHECKPOINT: earlier turns summarized"}`, enum.TooltypeOrigin)
			if err != nil {
				return err
			}
			m.AddToolCall(tc)
			m.SetFinishReason(enum.FinishReasonToolCalls)
			conv.Append(m)
			return nil
		}
	}
	m, _ := entity.NewMessage(enum.RoleAssistant, "done")
	m.SetFinishReason(enum.FinishReasonStop)
	conv.Append(m)
	return nil
}

// overflowSvc answers the first real call normally, 413s on the second —
// the provider's own verdict that our estimate missed — then serves the
// scratch dialogue and the retry.
type overflowSvc struct {
	inner      compactSvc
	realCalls  atomic.Int32
	sawCompact atomic.Bool
}

func (s *overflowSvc) Send(ctx context.Context, conv *aggregate.Conversation, gw *aggregate.Gateway, m entity.Model, h event.TurnEventHandler) error {
	for _, t := range conv.Tools() {
		if t.Name() == toolsummary.Name {
			s.sawCompact.Store(true)
			return s.inner.Send(ctx, conv, gw, m, h)
		}
	}
	if s.realCalls.Add(1) == 2 {
		return apperror.New(413, "context overflow", nil)
	}
	return s.inner.Send(ctx, conv, gw, m, h)
}

func compactApp(t *testing.T, svc dservice.TextModelService, contextWindow int) (*ConversationApp, drepo.Conversation) {
	t.Helper()
	gw, err := aggregate.NewGateway("gw1", "stub", "http://x", enum.StyleCompletion)
	if err != nil {
		t.Fatal(err)
	}
	gw.SetApiKey("sk")
	m, _ := entity.NewModel("m1", "gw1")
	m.SetContextWindow(contextWindow)
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
	convs := repoconversation.NewConversationRepository(t.TempDir())
	cfg := config.NewConfig()
	cfg.Conversation.CompactKeepTail = 2 // small tail keeps the test history tiny
	return New(convs, gateways,
		map[enum.Style]dservice.TextModelService{enum.StyleCompletion: svc},
		nil, cfg), convs
}

// Proactive: the estimate crosses the window share → the prefix seals
// and the turn answers on the compacted tail.
func TestSendCompactsOverflow(t *testing.T) {
	svc := &compactSvc{}
	app, convs := compactApp(t, svc, 60) // budget 54 — the embedded prompt alone overflows

	if _, err := app.Send(t.Context(), "room", "gw1", "m1", "first", nil, nil); err != nil {
		t.Fatal(err)
	}
	// Second send: 3 live messages > keepTail 2 → compaction must run.
	if _, err := app.Send(t.Context(), "room", "gw1", "m1", "second", nil, nil); err != nil {
		t.Fatal(err)
	}
	conv, err := convs.Load(t.Context(), "room")
	if err != nil {
		t.Fatal(err)
	}
	msgs := conv.Messages()
	if msgs[0].UserType() != enum.LLMUserTypeSystem || !strings.Contains(msgs[0].Content(), "CHECKPOINT") {
		t.Fatalf("head must be the continue checkpoint: %+v", msgs[0])
	}
	if conv.Last().Content() != "done" {
		t.Fatalf("compacted turn must still answer: %+v", msgs)
	}
	info, err := convs.Info(t.Context(), "room")
	if err != nil {
		t.Fatal(err)
	}
	if info.SealedSegments != 1 {
		t.Fatalf("prefix must seal exactly once: %+v", info)
	}
}

// deadEndSvc always 413s the real conversation — the provider that means
// it. The scratch dialogue still gets served.
type deadEndSvc struct {
	inner      compactSvc
	sawCompact atomic.Bool
}

func (s *deadEndSvc) Send(ctx context.Context, conv *aggregate.Conversation, gw *aggregate.Gateway, m entity.Model, h event.TurnEventHandler) error {
	for _, t := range conv.Tools() {
		if t.Name() == toolsummary.Name {
			s.sawCompact.Store(true)
			return s.inner.Send(ctx, conv, gw, m, h)
		}
	}
	return apperror.New(413, "context overflow", nil)
}

// Termination: a conversation the provider can never fit must not
// compact-loop — exactly one fold, then the 413 propagates.
func TestSend413DoesNotLoop(t *testing.T) {
	svc := &deadEndSvc{}
	app, convs := compactApp(t, svc, 0)

	if _, err := app.Send(t.Context(), "room", "gw1", "m1", "one", nil, nil); err == nil || apperror.As(err).StatusCode() != 413 {
		t.Fatalf("a lone message is unfoldable — 413 must propagate: %v", err)
	}
	if svc.sawCompact.Load() {
		t.Fatal("no compaction may run on a single message")
	}
	// A 413 never appends an assistant message — after two sends the
	// room holds two user lines, still at keepTail → no fold yet.
	if _, err := app.Send(t.Context(), "room", "gw1", "m1", "two", nil, nil); err == nil {
		t.Fatal("expected 413")
	}
	// Third send: 3 messages > keepTail → exactly one compact, then
	// the retried send still 413s and must surface, not re-compact.
	_, err := app.Send(t.Context(), "room", "gw1", "m1", "three", nil, nil)
	if err == nil || apperror.As(err).StatusCode() != 413 {
		t.Fatalf("persistent overflow must surface the provider's 413: %v", err)
	}
	info, _ := convs.Info(t.Context(), "room")
	if info.SealedSegments != 1 {
		t.Fatalf("exactly one segment may seal — no compact loop: %+v", info)
	}
}

// Reactive: the estimate fails open but the provider reports overflow —
// one forced compaction, then the retry succeeds.
func TestSendCompactsOn413(t *testing.T) {
	svc := &overflowSvc{}
	app, convs := compactApp(t, svc, 0) // unknown window → estimate never triggers

	if _, err := app.Send(t.Context(), "room", "gw1", "m1", "first", nil, nil); err != nil {
		t.Fatal(err)
	}
	if svc.sawCompact.Load() {
		t.Fatal("compaction must not run on a first overflow-free send")
	}
	if _, err := app.Send(t.Context(), "room", "gw1", "m1", "second", nil, nil); err != nil {
		t.Fatal(err)
	}
	if !svc.sawCompact.Load() {
		t.Fatal("413 must force exactly one compaction")
	}
	conv, _ := convs.Load(t.Context(), "room")
	if conv.Last().Content() != "done" {
		t.Fatalf("retry after compaction must answer: %+v", conv.Messages())
	}
	info, _ := convs.Info(t.Context(), "room")
	if info.SealedSegments != 1 {
		t.Fatalf("sealed: %+v", info)
	}
}
