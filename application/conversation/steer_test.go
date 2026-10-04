package appconversation

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	appjob "github.com/jahrulnr/nusallm/application/job"
	"github.com/jahrulnr/nusallm/infrastructure/config"
	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/event"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
	repoconversation "github.com/jahrulnr/nusallm/infrastructure/repository/conversation"
	repogateway "github.com/jahrulnr/nusallm/infrastructure/repository/gateway"
)

// gatedTool blocks in Execute until released — the deterministic window
// in which a steer is delivered mid-turn.
type gatedTool struct {
	entered chan struct{}
	release chan struct{}
}

func (g *gatedTool) Schema() valueobject.ToolSchema {
	s, _ := valueobject.NewToolSchema("gated", "blocks until released", nil, true, enum.ToolPermissionAgent)
	return s
}

func (g *gatedTool) Execute(ctx context.Context, _ entity.ToolCall, _ dservice.ToolEmitter) (dservice.ToolResult, error) {
	close(g.entered)
	select {
	case <-g.release:
		return dservice.ToolResult{Text: "tool output"}, nil
	case <-ctx.Done():
		return dservice.ToolResult{}, ctx.Err()
	}
}

// gatedScriptSvc demands the gated tool on call 1 and records the last
// message call 2 sees — where the folded steer must sit.
type gatedScriptSvc struct {
	calls    int32
	lastSeen string
}

func (s *gatedScriptSvc) Send(_ context.Context, conv *aggregate.Conversation, _ *aggregate.Gateway, _ entity.Model, _ event.TurnEventHandler) error {
	var m entity.Message
	if atomic.AddInt32(&s.calls, 1) == 1 {
		m, _ = entity.NewMessage(enum.RoleAssistant, "need a tool")
		tc, err := entity.NewToolCall("call_1", "gated", "{}", enum.TooltypeOrigin)
		if err != nil {
			return err
		}
		m.AddToolCall(tc)
		m.SetFinishReason(enum.FinishReasonToolCalls)
	} else {
		s.lastSeen = conv.Last().Content()
		m, _ = entity.NewMessage(enum.RoleAssistant, "done")
		m.SetFinishReason(enum.FinishReasonStop)
	}
	conv.Append(m)
	return nil
}

// gatedStack wires a room bound to the scripted provider and the gated
// tool — the send parks inside the tool until released, giving steer
// and recall a live turn to act on.
func gatedStack(t *testing.T) (*ConversationApp, *repoconversation.ConversationRepository, *gatedTool, *gatedScriptSvc) {
	t.Helper()
	entered := make(chan struct{})
	release := make(chan struct{})
	tool := &gatedTool{entered: entered, release: release}
	svc := &gatedScriptSvc{}
	gw, _ := aggregate.NewGateway("gw1", "stub", "http://unused", enum.StyleCompletion)
	gw.DisableStream()
	m, _ := entity.NewModel("m1", "gw1")
	gw.SetModels([]entity.Model{m})
	gateways := repogateway.NewGatewayRepository(t.TempDir(), nil)
	if err := gateways.Save(t.Context(), &gw); err != nil {
		t.Fatal(err)
	}
	convs := repoconversation.NewConversationRepository(t.TempDir())
	app := New(convs, gateways,
		map[enum.Environment]appjob.EnvironmentDriver{enum.EnvironmentLocal: appjob.EnvironmentBundle{
			Services: map[enum.Style]dservice.TextModelService{enum.StyleCompletion: svc},
			Tools:    map[string]dservice.Tool{"gated": tool},
		}},
		config.NewConfig(), nil, "")
	seed, _ := aggregate.NewConversation("room")
	seed.SetTools([]valueobject.ToolSchema{tool.Schema()})
	seed.SetRoute("gw1")
	seed.SetModelID("m1")
	if err := convs.Save(t.Context(), seed); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	return app, convs, tool, svc
}

// Steer delivered while a tool call runs folds into history ahead of
// the next provider call — the room's worker relays it to the job.
func TestSteerFoldsIntoRunningTurn(t *testing.T) {
	app, convs, tool, svc := gatedStack(t)

	sent := make(chan error, 1)
	go func() {
		_, err := app.Send(t.Context(), "room", "gw1", "m1", "run it", nil, appjob.SendOptions{}, nil)
		sent <- err
	}()
	<-tool.entered // the tool call is mid-execution — the turn is live

	if err := app.Steer(t.Context(), "room", "also check X"); err != nil {
		t.Fatal(err)
	}
	close(tool.release)
	select {
	case err := <-sent:
		if err != nil {
			t.Fatalf("send: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("turn never finished")
	}
	conv, err := convs.Load(t.Context(), "room")
	if err != nil {
		t.Fatal(err)
	}
	msgs := conv.Messages()
	// user / assistant+gated call / folded steer / assistant done
	if len(msgs) != 4 || msgs[2].Role() != enum.RoleUser || msgs[2].Content() != "also check X" {
		t.Fatalf("steer did not fold into history: %d msgs", len(msgs))
	}
	if svc.lastSeen != "also check X" {
		t.Fatalf("second provider call saw last=%q, want the steer", svc.lastSeen)
	}
}

// Recall takes the pending steer back before the fold — the turn never
// sees it and nothing lands in history.
func TestRecallSteer(t *testing.T) {
	app, convs, tool, svc := gatedStack(t)

	sent := make(chan error, 1)
	go func() {
		_, err := app.Send(t.Context(), "room", "gw1", "m1", "run it", nil, appjob.SendOptions{}, nil)
		sent <- err
	}()
	<-tool.entered

	if err := app.Steer(t.Context(), "room", "take this back"); err != nil {
		t.Fatal(err)
	}
	got, ok := app.RecallSteer("room")
	if !ok || got != "take this back" {
		t.Fatalf("recall = %q,%v", got, ok)
	}
	close(tool.release)
	select {
	case err := <-sent:
		if err != nil {
			t.Fatalf("send: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("turn never finished")
	}
	conv, err := convs.Load(t.Context(), "room")
	if err != nil {
		t.Fatal(err)
	}
	if len(conv.Messages()) != 3 {
		t.Fatalf("recalled steer entered history: %d msgs", len(conv.Messages()))
	}
	if svc.lastSeen != "need a tool" {
		t.Fatalf("recalled steer reached the model: last=%q", svc.lastSeen)
	}
	if _, ok := app.RecallSteer("room"); ok {
		t.Fatal("nothing pending must report no recall")
	}
}

// With no running turn there is nothing to steer into — 404, and an
// empty steer is rejected at the boundary.
func TestSteerNothingRunning(t *testing.T) {
	app, _ := fixture(t, okHandler)
	if err := app.Steer(t.Context(), "room", "hi"); apperror.As(err).StatusCode() != 404 {
		t.Fatalf("steer with no worker → %v, want 404", err)
	}
	if err := app.Steer(t.Context(), "room", "   "); apperror.As(err).StatusCode() != 400 {
		t.Fatalf("empty steer → %v, want 400", err)
	}
	if _, ok := app.RecallSteer("room"); ok {
		t.Fatal("recall on an idle room must report nothing")
	}
}
