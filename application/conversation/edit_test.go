package appconversation

import (
	"context"
	"encoding/json"
	"net/http"
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
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
	repoconversation "github.com/jahrulnr/nusallm/infrastructure/repository/conversation"
	repogateway "github.com/jahrulnr/nusallm/infrastructure/repository/gateway"
)

// Edit rewrites the target user message, drops the old assistant suffix,
// and regenerates — the room ends as [edited user, context record, fresh
// assistant].
func TestEditRegeneratesFromTarget(t *testing.T) {
	app, _ := fixture(t, okHandler)
	if _, err := app.Send(t.Context(), "room", "gw1", "m1", "original", nil, appjob.SendOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Send(t.Context(), "room", "", "", "second", nil, appjob.SendOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	conv, _ := app.deps.Convs.Load(t.Context(), "room")
	target := conv.Messages()[0].ID() // first user message

	if err := app.Edit(t.Context(), "room", "", "", target, "edited", appjob.SendOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	conv, err := app.deps.Convs.Load(t.Context(), "room")
	if err != nil {
		t.Fatal(err)
	}
	msgs := conv.Messages()
	if len(msgs) != 3 || msgs[0].Content() != "edited" || msgs[0].ID() != target {
		t.Fatalf("edited history wrong: %+v", msgs)
	}
	if msgs[2].Role() != enum.RoleAssistant {
		t.Fatal("edit must regenerate the assistant turn")
	}
}

// Empty replacement text is rejected before the job ever posts — the
// use case owns the submittable rule, same as Send.
func TestEditRejectsEmpty(t *testing.T) {
	app, _ := fixture(t, okHandler)
	for _, content := range []string{"", "   "} {
		if err := app.Edit(t.Context(), "room", "", "", "m_x", content, appjob.SendOptions{}, nil); apperror.As(err).StatusCode() != 400 {
			t.Fatalf("empty edit %q → %v, want 400", content, err)
		}
	}
	waitWorkers(t, app, 0)
}

func TestEditRejectsNonUser(t *testing.T) {
	app, _ := fixture(t, okHandler)
	if _, err := app.Send(t.Context(), "room", "gw1", "m1", "hi", nil, appjob.SendOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	conv, _ := app.deps.Convs.Load(t.Context(), "room")
	assistant := conv.Messages()[2].ID()
	if err := app.Edit(t.Context(), "room", "", "", assistant, "hijack", appjob.SendOptions{}, nil); apperror.As(err).StatusCode() != 400 {
		t.Fatalf("assistant edit → %v, want 400", err)
	}
	// The workspace context record is a non-editable user-role message —
	// the same predicate excludes it.
	contextRecord := conv.Messages()[1].ID()
	if err := app.Edit(t.Context(), "room", "", "", contextRecord, "hijack", appjob.SendOptions{}, nil); apperror.As(err).StatusCode() != 400 {
		t.Fatalf("context-record edit → %v, want 400", err)
	}
}

func TestEditNotFound(t *testing.T) {
	app, _ := fixture(t, okHandler)
	if _, err := app.Send(t.Context(), "room", "gw1", "m1", "hi", nil, appjob.SendOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	if err := app.Edit(t.Context(), "room", "", "", "m_nope", "x", appjob.SendOptions{}, nil); apperror.As(err).StatusCode() != 404 {
		t.Fatalf("missing id → %v, want 404", err)
	}
	if err := app.Edit(t.Context(), "ghost", "", "", "m_x", "x", appjob.SendOptions{}, nil); apperror.As(err).StatusCode() != 404 {
		t.Fatalf("missing room → %v, want 404", err)
	}
	waitWorkers(t, app, 0)
}

// The headline edge case: the edited message lives in a sealed segment.
// The worker repromotes its era and regenerates from there.
func TestEditCompactedMessage(t *testing.T) {
	app, _ := fixture(t, okHandler)
	if _, err := app.Send(t.Context(), "room", "gw1", "m1", "old question", nil, appjob.SendOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	conv, _ := app.deps.Convs.Load(t.Context(), "room")
	target := conv.Messages()[0].ID()

	// Seal the first turn — the user message leaves the live tail.
	sum, _ := entity.NewMessage(enum.RoleUser, "summary of old turn")
	sum.SetUserType(enum.LLMUserTypeSystem)
	if err := app.deps.Convs.Compact(t.Context(), "room", sum, 1, conv.Messages()[1:]); err != nil {
		t.Fatal(err)
	}
	conv, _ = app.deps.Convs.Load(t.Context(), "room")
	if conv.Messages()[0].Content() == "old question" {
		t.Fatal("setup failed — target must be sealed out of the live tail")
	}

	if err := app.Edit(t.Context(), "room", "", "", target, "new question", appjob.SendOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	conv, _ = app.deps.Convs.Load(t.Context(), "room")
	msgs := conv.Messages()
	if len(msgs) != 3 || msgs[0].ID() != target || msgs[0].Content() != "new question" {
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
	if _, err := app.Send(t.Context(), "room", "gw1", "m1", "v1", nil, appjob.SendOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	conv, _ := app.deps.Convs.Load(t.Context(), "room")
	target := conv.Messages()[0].ID()

	sent := make(chan error, 1)
	go func() {
		_, err := app.Send(t.Context(), "room", "", "", "v2", nil, appjob.SendOptions{}, nil)
		sent <- err
	}()
	// The second request blocks upstream — calls==2 means the send is
	// provably holding the slot before the edit preempts it.
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&calls) != 2 {
		if time.Now().After(deadline) {
			t.Fatal("second send did not reach the upstream")
		}
		time.Sleep(5 * time.Millisecond)
	}

	if err := app.Edit(t.Context(), "room", "", "", target, "edited", appjob.SendOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-sent:
		if apperror.As(err).StatusCode() != 408 {
			t.Fatalf("preempted send → %v, want 408", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("preempted send never returned")
	}
	conv, _ = app.deps.Convs.Load(t.Context(), "room")
	msgs := conv.Messages()
	if len(msgs) != 3 || msgs[0].Content() != "edited" {
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
		map[enum.Environment]appjob.EnvironmentDriver{enum.EnvironmentLocal: appjob.EnvironmentBundle{
			Services: map[enum.Style]dservice.TextModelService{enum.StyleCompletion: svc},
			Tools:    map[string]dservice.Tool{"blocking": tool},
		}},
		config.NewConfig(), nil, "", nil, nil)
	seed, _ := aggregate.NewConversation("room")
	seed.SetTools([]valueobject.ToolSchema{tool.Schema()})
	seed.SetRoute("gw1")
	seed.SetModelID("m1") // binding must pre-exist — Edit resolves route "" from it
	if err := convs.Save(t.Context(), seed); err != nil {
		t.Fatal(err)
	}

	sent := make(chan error, 1)
	go func() {
		_, err := app.Send(t.Context(), "room", "gw1", "m1", "run it", nil, appjob.SendOptions{}, nil)
		sent <- err
	}()
	<-entered // the tool call is mid-execution

	conv, _ := convs.Load(t.Context(), "room")
	target := conv.Messages()[0].ID()
	if err := app.Edit(t.Context(), "room", "", "", target, "edited instead", appjob.SendOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-sent:
		if apperror.As(err).StatusCode() != 408 {
			t.Fatalf("preempted send → %v, want 408", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("edit did not interrupt the running tool")
	}
	conv, _ = convs.Load(t.Context(), "room")
	msgs := conv.Messages()
	if len(msgs) != 3 || msgs[0].Content() != "edited instead" || msgs[2].Content() != "done" {
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

// A turn that fails on the bound model must be retryable on another
// one: the edit carries the caller's new selection upstream, and the
// successful regenerate re-binds the room to it. Before route/modelID
// rode turn.edit, a retry always re-resolved the binding that failed.
func TestEditRetriesWithNewModelSelection(t *testing.T) {
	var mu sync.Mutex
	var models []string
	app, gw := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		models = append(models, body.Model)
		mu.Unlock()
		if body.Model == "m1" {
			w.WriteHeader(http.StatusPaymentRequired) // balance exhausted
			return
		}
		okHandler(w, r)
	})
	m1, _ := entity.NewModel("m1", "gw1")
	m2, _ := entity.NewModel("m2", "gw1")
	gw.SetModels([]entity.Model{m1, m2})
	if err := app.deps.Gateways.Save(t.Context(), gw); err != nil {
		t.Fatal(err)
	}

	// m1 dies upstream — the fresh room still binds it, the failure the
	// picker-driven retry must escape.
	if _, err := app.Send(t.Context(), "room", "gw1", "m1", "hi", nil, appjob.SendOptions{}, nil); err == nil {
		t.Fatal("m1 send must fail")
	}
	conv, err := app.deps.Convs.Load(t.Context(), "room")
	if err != nil {
		t.Fatal(err)
	}
	target := conv.Messages()[0].ID()

	if err := app.Edit(t.Context(), "room", "gw1", "m2", target, "hi", appjob.SendOptions{}, nil); err != nil {
		t.Fatalf("edit retry on m2: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(models) != 2 || models[0] != "m1" || models[1] != "m2" {
		t.Fatalf("upstream models = %v, want [m1 m2]", models)
	}
	after, err := app.deps.Convs.Load(t.Context(), "room")
	if err != nil {
		t.Fatal(err)
	}
	if after.ModelID() != "m2" {
		t.Fatalf("room binding = %q, want m2 after a successful retry", after.ModelID())
	}
}

// An override workspace is validated before the rewrite but bound
// after it: a sealed target swaps in a fresh aggregate, and a workspace
// set on the old handle would be lost with it. The regenerated room
// must carry the override.
func TestEditSealedTargetBindsOverrideWorkspace(t *testing.T) {
	gw, _ := aggregate.NewGateway("gw1", "stub", "http://unused", enum.StyleCompletion)
	m, _ := entity.NewModel("m1", "gw1")
	gw.SetModels([]entity.Model{m})
	gateways := repogateway.NewGatewayRepository(t.TempDir(), nil)
	if err := gateways.Save(t.Context(), &gw); err != nil {
		t.Fatal(err)
	}
	convs := repoconversation.NewConversationRepository(t.TempDir())
	app := New(convs, gateways,
		map[enum.Environment]appjob.EnvironmentDriver{enum.EnvironmentLocal: appjob.EnvironmentBundle{
			WorkspaceStore: virtualWorkspace{},
			Services:       map[enum.Style]dservice.TextModelService{enum.StyleCompletion: environmentService{label: "local"}},
			Tools:          map[string]dservice.Tool{"echo": environmentTool{label: "local"}},
		}},
		config.NewConfig(), nil, "", nil, nil)
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })

	if _, err := app.Send(t.Context(), "room", "gw1", "m1", "old question", nil, appjob.SendOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	conv, err := convs.Load(t.Context(), "room")
	if err != nil {
		t.Fatal(err)
	}
	target := conv.Messages()[0].ID()

	// Seal the first turn — the edit repromotes and reloads the aggregate.
	sum, _ := entity.NewMessage(enum.RoleUser, "summary of old turn")
	sum.SetUserType(enum.LLMUserTypeSystem)
	if err := convs.Compact(t.Context(), "room", sum, 1, conv.Messages()[1:]); err != nil {
		t.Fatal(err)
	}

	err = app.Edit(t.Context(), "room", "", "", target, "moved",
		appjob.SendOptions{Workspace: "/remote/project"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	after, err := convs.Load(t.Context(), "room")
	if err != nil {
		t.Fatal(err)
	}
	if after.Workspace() != "/remote/project" {
		t.Fatalf("sealed-target edit dropped the workspace override: %q", after.Workspace())
	}
}

// An override that fails keeps the old binding — the same
// re-bind-only-on-success rule Send already follows.
func TestEditFailedOverrideKeepsBinding(t *testing.T) {
	app, gw := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Model == "m2" {
			w.WriteHeader(http.StatusPaymentRequired) // definitive failure, no retry
			return
		}
		okHandler(w, r)
	})
	m1, _ := entity.NewModel("m1", "gw1")
	m2, _ := entity.NewModel("m2", "gw1")
	gw.SetModels([]entity.Model{m1, m2})
	if err := app.deps.Gateways.Save(t.Context(), gw); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Send(t.Context(), "room", "gw1", "m1", "hi", nil, appjob.SendOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	conv, err := app.deps.Convs.Load(t.Context(), "room")
	if err != nil {
		t.Fatal(err)
	}
	target := conv.Messages()[0].ID()
	if err := app.Edit(t.Context(), "room", "gw1", "m2", target, "hi", appjob.SendOptions{}, nil); err == nil {
		t.Fatal("edit on failing m2 must fail")
	}
	after, err := app.deps.Convs.Load(t.Context(), "room")
	if err != nil {
		t.Fatal(err)
	}
	if after.ModelID() != "m1" {
		t.Fatalf("failed override re-bound the room: %q", after.ModelID())
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

// The driver is resolved before any persistent rewrite: an edit in a room
// whose environment is unavailable must fail without touching history.
func TestEditResolvesEnvironmentBeforeRewrite(t *testing.T) {
	gw, _ := aggregate.NewGateway("gw1", "stub", "http://unused", enum.StyleCompletion)
	m, _ := entity.NewModel("m1", "gw1")
	gw.SetModels([]entity.Model{m})
	gateways := repogateway.NewGatewayRepository(t.TempDir(), nil)
	if err := gateways.Save(t.Context(), &gw); err != nil {
		t.Fatal(err)
	}
	convs := repoconversation.NewConversationRepository(t.TempDir())
	seed, err := aggregate.NewConversation("room")
	if err != nil {
		t.Fatal(err)
	}
	seed.SetRoute("gw1")
	seed.SetModelID("m1")
	if err := convs.Save(t.Context(), seed); err != nil {
		t.Fatal(err)
	}
	user, _ := entity.NewMessage(enum.RoleUser, "original")
	if _, err := convs.AppendMessage(t.Context(), "room", user); err != nil {
		t.Fatal(err)
	}
	assistant, _ := entity.NewMessage(enum.RoleAssistant, "reply")
	if _, err := convs.AppendMessage(t.Context(), "room", assistant); err != nil {
		t.Fatal(err)
	}

	app := New(convs, gateways, map[enum.Environment]appjob.EnvironmentDriver{}, config.NewConfig(), nil, "", nil, nil)
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })

	if err := app.Edit(t.Context(), "room", "", "", user.ID(), "edited", appjob.SendOptions{}, nil); apperror.As(err).StatusCode() != 400 {
		t.Fatalf("edit with unavailable environment => %v, want 400", err)
	}

	after, err := convs.Load(t.Context(), "room")
	if err != nil {
		t.Fatal(err)
	}
	msgs := after.Messages()
	if len(msgs) != 2 || msgs[0].Content() != "original" || msgs[1].Content() != "reply" {
		t.Fatalf("failed edit mutated history: %+v", msgs)
	}
}
