package appconversation

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	appjob "github.com/jahrulnr/nusallm/application/job"
	"github.com/jahrulnr/nusallm/infrastructure/config"
	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/event"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
	repoconversation "github.com/jahrulnr/nusallm/infrastructure/repository/conversation"
	repogateway "github.com/jahrulnr/nusallm/infrastructure/repository/gateway"
	reposecret "github.com/jahrulnr/nusallm/infrastructure/repository/secret"
	toolsummary "github.com/jahrulnr/nusallm/infrastructure/service/tool/summary"
)

// compactSvc answers the scratch dialogue (the conv carrying the summary
// tool) with the checkpoint call, and any other conversation normally.
type compactSvc struct{ calls atomic.Int32 }

type environmentCompactSvc struct {
	compactSvc
	scratchEnvironment enum.Environment
	scratchWorkspace   string
}

func (s *environmentCompactSvc) Send(ctx context.Context, conv *aggregate.Conversation, gw *aggregate.Gateway, model entity.Model, onEvent event.TurnEventHandler) error {
	for _, tool := range conv.Tools() {
		if tool.Name() == toolsummary.ToolName {
			s.scratchEnvironment = conv.Environment()
			s.scratchWorkspace = conv.Workspace()
		}
	}
	return s.compactSvc.Send(ctx, conv, gw, model, onEvent)
}

func TestManualCompactKeepsEnvironmentAndWorkspace(t *testing.T) {
	local := &compactSvc{}
	app, convs := compactApp(t, local, 0)
	remote := &environmentCompactSvc{}
	app.deps.Environments[enum.EnvironmentDocker] = appjob.EnvironmentBundle{
		Services:       map[enum.Style]dservice.TextModelService{enum.StyleCompletion: remote},
		WorkspaceStore: virtualWorkspace{},
	}
	for _, content := range []string{"first", "second"} {
		opts := appjob.SendOptions{}
		if content == "first" {
			opts.Environment = enum.EnvironmentDocker
			opts.Workspace = "/remote/project"
		}
		if _, err := app.Send(t.Context(), "remote-room", "gw1", "m1", content, nil, opts, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := app.Compact(t.Context(), "remote-room"); err != nil {
		t.Fatal(err)
	}
	if local.calls.Load() != 0 || remote.scratchEnvironment != enum.EnvironmentDocker || remote.scratchWorkspace != "/remote/project" {
		t.Fatalf("compaction crossed drivers: local=%d scratch=%q %q", local.calls.Load(), remote.scratchEnvironment, remote.scratchWorkspace)
	}
	loaded, err := convs.Load(t.Context(), "remote-room")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Environment() != enum.EnvironmentDocker || loaded.Workspace() != "/remote/project" {
		t.Fatalf("compacted binding = %q %q", loaded.Environment(), loaded.Workspace())
	}
}

func (s *compactSvc) Send(_ context.Context, conv *aggregate.Conversation, _ *aggregate.Gateway, _ entity.Model, _ event.TurnEventHandler) error {
	s.calls.Add(1)
	for _, t := range conv.Tools() {
		if t.Name() == toolsummary.ToolName {
			m, _ := entity.NewMessage(enum.RoleAssistant, "")
			tc, err := entity.NewToolCall("sum_1", toolsummary.ToolName, `{"text":"CHECKPOINT: earlier turns summarized"}`, enum.TooltypeOrigin)
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
		if t.Name() == toolsummary.ToolName {
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
		map[enum.Environment]appjob.EnvironmentDriver{enum.EnvironmentLocal: appjob.EnvironmentBundle{
			Services: map[enum.Style]dservice.TextModelService{enum.StyleCompletion: svc},
		}}, cfg, nil, "", nil, mustSummaryTool(t)), convs
}

// mustSummaryTool builds the injected compaction contract for test Deps.
func mustSummaryTool(t *testing.T) dservice.Tool {
	t.Helper()
	tool, err := toolsummary.NewSummaryTool()
	if err != nil {
		t.Fatal(err)
	}
	return tool
}

func TestManualCompactAllowsAnotherFoldWhileTailExceedsKeep(t *testing.T) {
	svc := &compactSvc{}
	app, convs := compactApp(t, svc, 0)
	for _, content := range []string{"first", "second"} {
		if _, err := app.Send(t.Context(), "room", "gw1", "m1", content, nil, appjob.SendOptions{}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if available, err := app.CanCompact(t.Context(), "room"); err != nil || !available {
		t.Fatalf("expected foldable history, available=%v err=%v", available, err)
	}
	if err := app.Compact(t.Context(), "room"); err != nil {
		t.Fatal(err)
	}
	// The checkpoint plus the kept tail is still longer than keep-tail,
	// so a second explicit fold is a real compaction, not an error.
	if available, err := app.CanCompact(t.Context(), "room"); err != nil || !available {
		t.Fatalf("tail still longer than keep-tail must stay foldable, available=%v err=%v", available, err)
	}
	if err := app.Compact(t.Context(), "room"); err != nil {
		t.Fatal(err)
	}
	info, err := convs.Info(t.Context(), "room")
	if err != nil || info.SealedSegments != 2 {
		t.Fatalf("second compact sealed segments: %+v %v", info, err)
	}
	if _, err := app.Send(t.Context(), "room", "gw1", "m1", "third", nil, appjob.SendOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	if available, err := app.CanCompact(t.Context(), "room"); err != nil || !available {
		t.Fatalf("new history must permit another compact, available=%v err=%v", available, err)
	}
}

// Proactive: the estimate crosses the window share → the prefix seals
// and the turn answers on the compacted tail.
func TestSendCompactsOverflow(t *testing.T) {
	svc := &compactSvc{}
	// 64_060 total → 60 input allowance → budget 54 at 90% — same headroom as the old 60×90% test.
	app, convs := compactApp(t, svc, 64_060)

	if _, err := app.Send(t.Context(), "room", "gw1", "m1", "first", nil, appjob.SendOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	// Second send: 3 live messages > keepTail 2 → compaction must run.
	if _, err := app.Send(t.Context(), "room", "gw1", "m1", "second", nil, appjob.SendOptions{}, nil); err != nil {
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
		if t.Name() == toolsummary.ToolName {
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

	if _, err := app.Send(t.Context(), "room", "gw1", "m1", "one", nil, appjob.SendOptions{}, nil); err == nil || apperror.As(err).StatusCode() != 413 {
		t.Fatalf("a lone message is unfoldable — 413 must propagate: %v", err)
	}
	if svc.sawCompact.Load() {
		t.Fatal("no compaction may run on a single message")
	}
	// Each failed send leaves a user line plus its context record — the
	// second send crosses keepTail → exactly one fold, then the retried
	// call still 413s and must surface, not re-compact.
	if _, err := app.Send(t.Context(), "room", "gw1", "m1", "two", nil, appjob.SendOptions{}, nil); err == nil || apperror.As(err).StatusCode() != 413 {
		t.Fatalf("persistent overflow must surface the provider's 413: %v", err)
	}
	if !svc.sawCompact.Load() {
		t.Fatal("the second send must force exactly one compaction")
	}
	// Third send: the history crosses keepTail again → one more fold —
	// each turn folds at most once, never loop-compacts within a turn.
	_, err := app.Send(t.Context(), "room", "gw1", "m1", "three", nil, appjob.SendOptions{}, nil)
	if err == nil || apperror.As(err).StatusCode() != 413 {
		t.Fatalf("persistent overflow must surface the provider's 413: %v", err)
	}
	info, _ := convs.Info(t.Context(), "room")
	if info.SealedSegments != 2 {
		t.Fatalf("one segment per failed send may seal — no compact loop: %+v", info)
	}
}

// Reactive: the estimate fails open but the provider reports overflow —
// one forced compaction, then the retry succeeds.
func TestSendCompactsOn413(t *testing.T) {
	svc := &overflowSvc{}
	app, convs := compactApp(t, svc, 0) // unknown window → proactive budget is high; this path tests 413

	if _, err := app.Send(t.Context(), "room", "gw1", "m1", "first", nil, appjob.SendOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	if svc.sawCompact.Load() {
		t.Fatal("compaction must not run on a first overflow-free send")
	}
	if _, err := app.Send(t.Context(), "room", "gw1", "m1", "second", nil, appjob.SendOptions{}, nil); err != nil {
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
