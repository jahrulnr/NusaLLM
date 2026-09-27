package appconversation

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

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

// Edit rewrites the target user message, drops the old assistant suffix,
// and regenerates — the room ends as [edited user, fresh assistant].
func TestEditRegeneratesFromTarget(t *testing.T) {
	app, _ := fixture(t, okHandler)
	if _, err := app.Send(t.Context(), "room", "gw1", "m1", "original", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Send(t.Context(), "room", "", "", "second", nil, nil); err != nil {
		t.Fatal(err)
	}
	conv, _ := app.deps.Convs.Load(t.Context(), "room")
	target := conv.Messages()[0].ID() // first user message

	if err := app.Edit(t.Context(), "room", target, "edited", nil); err != nil {
		t.Fatal(err)
	}
	conv, err := app.deps.Convs.Load(t.Context(), "room")
	if err != nil {
		t.Fatal(err)
	}
	msgs := conv.Messages()
	if len(msgs) != 2 || msgs[0].Content() != "edited" || msgs[0].ID() != target {
		t.Fatalf("edited history wrong: %+v", msgs)
	}
	if msgs[1].Role() != enum.RoleAssistant {
		t.Fatal("edit must regenerate the assistant turn")
	}
}

func TestEditRejectsNonUser(t *testing.T) {
	app, _ := fixture(t, okHandler)
	if _, err := app.Send(t.Context(), "room", "gw1", "m1", "hi", nil, nil); err != nil {
		t.Fatal(err)
	}
	conv, _ := app.deps.Convs.Load(t.Context(), "room")
	assistant := conv.Messages()[1].ID()
	if err := app.Edit(t.Context(), "room", assistant, "hijack", nil); apperror.As(err).StatusCode() != 400 {
		t.Fatalf("assistant edit → %v, want 400", err)
	}
}

func TestEditNotFound(t *testing.T) {
	app, _ := fixture(t, okHandler)
	if _, err := app.Send(t.Context(), "room", "gw1", "m1", "hi", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := app.Edit(t.Context(), "room", "m_nope", "x", nil); apperror.As(err).StatusCode() != 404 {
		t.Fatalf("missing id → %v, want 404", err)
	}
	if err := app.Edit(t.Context(), "ghost", "m_x", "x", nil); apperror.As(err).StatusCode() != 404 {
		t.Fatalf("missing room → %v, want 404", err)
	}
	waitWorkers(t, app, 0)
}

// The headline edge case: the edited message lives in a sealed segment.
// The worker repromotes its era and regenerates from there.
func TestEditCompactedMessage(t *testing.T) {
	app, _ := fixture(t, okHandler)
	if _, err := app.Send(t.Context(), "room", "gw1", "m1", "old question", nil, nil); err != nil {
		t.Fatal(err)
	}
	conv, _ := app.deps.Convs.Load(t.Context(), "room")
	target := conv.Messages()[0].ID()

	// Seal the first turn — the user message leaves the live tail.
	sum, _ := entity.NewMessage(enum.RoleUser, "summary of old turn")
	sum.SetUserType(enum.LLMUserTypeSystem)
	if err := app.deps.Convs.Compact(t.Context(), "room", sum, 1); err != nil {
		t.Fatal(err)
	}
	conv, _ = app.deps.Convs.Load(t.Context(), "room")
	if conv.Messages()[0].Content() == "old question" {
		t.Fatal("setup failed — target must be sealed out of the live tail")
	}

	if err := app.Edit(t.Context(), "room", target, "new question", nil); err != nil {
		t.Fatal(err)
	}
	conv, _ = app.deps.Convs.Load(t.Context(), "room")
	msgs := conv.Messages()
	if len(msgs) != 2 || msgs[0].ID() != target || msgs[0].Content() != "new question" {
		t.Fatalf("repromoted history wrong: %+v", msgs)
	}
	info, _ := app.deps.Convs.Info(t.Context(), "room")
	if info.SealedSegments != 0 {
		t.Fatalf("sealed era must be promoted: %+v", info)
	}
}

// Edit preempts a live turn: the running send is canceled (408) and the
// edit runs on the truncated history.
func TestEditPreemptsRunningSend(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	var calls int32
	app, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 2 {
			<-release // hold the second response — the send stays live
		}
		okHandler(w, r)
	})
	// Seed the room so the edit has a target.
	if _, err := app.Send(t.Context(), "room", "gw1", "m1", "v1", nil, nil); err != nil {
		t.Fatal(err)
	}
	conv, _ := app.deps.Convs.Load(t.Context(), "room")
	target := conv.Messages()[0].ID()

	sent := make(chan error, 1)
	go func() { _, err := app.Send(t.Context(), "room", "", "", "v2", nil, nil); sent <- err }()
	time.Sleep(50 * time.Millisecond) // send is holding the slot

	if err := app.Edit(t.Context(), "room", target, "edited", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-sent:
		if apperror.As(err).StatusCode() != 408 {
			t.Fatalf("preempted send → %v, want 408", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("preempted send never returned")
	}
	conv, _ = app.deps.Convs.Load(t.Context(), "room")
	msgs := conv.Messages()
	if len(msgs) != 2 || msgs[0].Content() != "edited" {
		t.Fatalf("post-edit history wrong: %+v", msgs)
	}
}

// Edit while a tool call is executing: the tool's ctx dies, the send
// reports 408, and the pending call is truncated away by the rewrite.
func TestEditPreemptsRunningTool(t *testing.T) {
	entered := make(chan struct{})
	tool := &blockingTool{entered: entered}
	svc := &blockingScriptSvc{}
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
		map[enum.Style]dservice.TextModelService{enum.StyleCompletion: svc},
		map[string]dservice.Tool{"blocking": tool},
		config.NewConfig(), "")
	seed, _ := aggregate.NewConversation("room")
	seed.SetTools([]valueobject.ToolSchema{tool.Schema()})
	seed.SetRoute("gw1")
	seed.SetModelID("m1") // binding must pre-exist — Edit resolves route "" from it
	if err := convs.Save(t.Context(), seed); err != nil {
		t.Fatal(err)
	}

	sent := make(chan error, 1)
	go func() { _, err := app.Send(t.Context(), "room", "gw1", "m1", "run it", nil, nil); sent <- err }()
	<-entered // the tool call is mid-execution

	conv, _ := convs.Load(t.Context(), "room")
	target := conv.Messages()[0].ID()
	if err := app.Edit(t.Context(), "room", target, "edited instead", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-sent:
		if apperror.As(err).StatusCode() != 408 {
			t.Fatalf("preempted send → %v, want 408", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("edit did not interrupt the running tool")
	}
	conv, _ = convs.Load(t.Context(), "room")
	msgs := conv.Messages()
	if len(msgs) != 2 || msgs[0].Content() != "edited instead" || msgs[1].Content() != "done" {
		t.Fatalf("post-edit history wrong: %+v", msgs)
	}
	// The truncated suffix must not leave a pending call behind.
	for _, m := range msgs {
		for _, tc := range m.ToolCalls() {
			if tc.Output() == "" {
				t.Fatalf("orphaned pending call survived the rewrite: %+v", tc)
			}
		}
	}
}

// blockingTool blocks in Execute until its ctx dies — a stuck tool.
type blockingTool struct{ entered chan struct{} }

func (b *blockingTool) Schema() valueobject.ToolSchema {
	s, _ := valueobject.NewToolSchema("blocking", "blocks forever", nil, true, enum.ToolPermissionAgent)
	return s
}

func (b *blockingTool) Execute(ctx context.Context, _ entity.ToolCall, _ dservice.ToolEmitter) (dservice.ToolResult, error) {
	close(b.entered)
	<-ctx.Done()
	return dservice.ToolResult{}, ctx.Err()
}

// blockingScriptSvc scripts a stuck tool: first call demands the
// "blocking" tool, any later call (the post-edit resend) answers "done".
type blockingScriptSvc struct{ calls int32 }

func (s *blockingScriptSvc) Send(_ context.Context, conv *aggregate.Conversation, _ *aggregate.Gateway, _ entity.Model, _ event.TurnEventHandler) error {
	var m entity.Message
	if atomic.AddInt32(&s.calls, 1) == 1 {
		m, _ = entity.NewMessage(enum.RoleAssistant, "need a tool")
		tc, err := entity.NewToolCall("call_1", "blocking", "{}", enum.TooltypeOrigin)
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
