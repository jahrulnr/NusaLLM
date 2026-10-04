package job

import (
	"context"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/config"
	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/event"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
	repoconversation "github.com/jahrulnr/nusallm/infrastructure/repository/conversation"
)

// steerFoldSvc asks for the "push" tool on the first provider call and
// answers "done" on the second, recording the last message it saw —
// proving where the folded steer sits relative to the tool result.
type steerFoldSvc struct {
	calls    int32
	lastSeen string
}

func (s *steerFoldSvc) Send(_ context.Context, conv *aggregate.Conversation, _ *aggregate.Gateway, _ entity.Model, _ event.TurnEventHandler) error {
	n := atomic.AddInt32(&s.calls, 1)
	var m entity.Message
	if n == 1 {
		m, _ = entity.NewMessage(enum.RoleAssistant, "need a tool")
		tc, err := entity.NewToolCall("call_1", "push", `{}`, enum.TooltypeOrigin)
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

// pushTool answers instantly; its onExec hook runs inside the turn's
// tool phase, which is exactly when a mid-run steer is delivered.
type pushTool struct {
	schema valueobject.ToolSchema
	onExec func()
}

func (t pushTool) Schema() valueobject.ToolSchema { return t.schema }
func (t pushTool) Execute(context.Context, entity.ToolCall, dservice.ToolEmitter) (dservice.ToolResult, error) {
	if t.onExec != nil {
		t.onExec()
	}
	return dservice.ToolResult{Text: "tool output"}, nil
}

// A steer pushed while a tool executes folds at the top of the next
// step — after the tool result persisted, before the second provider
// call — as an ordinary user message with a steer event.
func TestRunTurnFoldsSteerAfterToolResult(t *testing.T) {
	d := Deps{
		Convs:  repoconversation.NewConversationRepository(t.TempDir()),
		Config: config.ConversationConfig{MaxTurnStep: 4},
	}
	conv, _ := aggregate.NewConversation("room")
	user, _ := entity.NewMessage(enum.RoleUser, "go")
	conv.Append(user)
	if err := d.Convs.Save(t.Context(), conv); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Convs.AppendMessage(t.Context(), "room", user); err != nil {
		t.Fatal(err)
	}
	steers := &steerInbox{}
	schema, _ := valueobject.NewToolSchema("push", "test tool", nil, true, enum.ToolPermissionAgent)
	driver := EnvironmentBundle{Tools: map[string]dservice.Tool{
		"push": pushTool{schema: schema, onExec: func() { steers.push("also handle X") }},
	}}
	svc := &steerFoldSvc{}
	var kinds []enum.TurnEventKind
	onEvent := func(e event.TurnEvent) { kinds = append(kinds, e.Kind()) }
	if err := d.runTurn(t.Context(), "room", conv, driver, nil, entity.Model{}, svc, onEvent, steers); err != nil {
		t.Fatal(err)
	}
	msgs := conv.Messages()
	// user "go" / assistant+push call / folded steer / assistant "done"
	if len(msgs) != 4 {
		t.Fatalf("folded history = %d msgs, want 4", len(msgs))
	}
	if msgs[2].Role() != enum.RoleUser || msgs[2].Content() != "also handle X" {
		t.Fatalf("steer message = %v %q", msgs[2].Role(), msgs[2].Content())
	}
	if !msgs[1].ToolCalls()[0].HasOutput() {
		t.Fatal("the tool result must be recorded before the steer folded")
	}
	if svc.lastSeen != "also handle X" {
		t.Fatalf("provider call 2 saw last=%q, want the folded steer", svc.lastSeen)
	}
	if !slices.Contains(kinds, enum.TurnEventSteer) {
		t.Fatalf("no steer event emitted: %v", kinds)
	}
	stored, err := d.Convs.Load(t.Context(), "room")
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Messages()) != 4 || stored.Messages()[2].Content() != "also handle X" {
		t.Fatalf("persisted history missing the fold: %d msgs", len(stored.Messages()))
	}
}

// captureSvc records the last message the provider call saw — used to
// prove a pre-start steer was already in history at call one.
type captureSvc struct{ lastSeen string }

func (s *captureSvc) Send(_ context.Context, conv *aggregate.Conversation, _ *aggregate.Gateway, _ entity.Model, _ event.TurnEventHandler) error {
	s.lastSeen = conv.Last().Content()
	m, _ := entity.NewMessage(enum.RoleAssistant, "done")
	m.SetFinishReason(enum.FinishReasonStop)
	conv.Append(m)
	return nil
}

// Input posted before the job even starts — a steer queued behind the
// worker's earlier work — folds at step zero, before the first call.
func TestRunTurnFoldsSteerBeforeFirstCall(t *testing.T) {
	d := Deps{
		Convs:  repoconversation.NewConversationRepository(t.TempDir()),
		Config: config.ConversationConfig{MaxTurnStep: 4},
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
	steers := &steerInbox{}
	steers.push("early input")
	svc := &captureSvc{}
	if err := d.runTurn(t.Context(), "room", conv, EnvironmentBundle{}, nil, entity.Model{}, svc, nil, steers); err != nil {
		t.Fatal(err)
	}
	if svc.lastSeen != "early input" {
		t.Fatalf("provider call 1 saw last=%q, want the pre-folded steer", svc.lastSeen)
	}
	msgs := conv.Messages()
	if len(msgs) != 3 || msgs[1].Role() != enum.RoleUser || msgs[1].Content() != "early input" {
		t.Fatalf("pre-start steer did not fold: %+v", msgs)
	}
}

// doneSvc answers immediately; its onSend hook runs inside the single
// provider call — a steer pushed there has no further step to fold at.
type doneSvc struct{ onSend func() }

func (s doneSvc) Send(_ context.Context, conv *aggregate.Conversation, _ *aggregate.Gateway, _ entity.Model, _ event.TurnEventHandler) error {
	if s.onSend != nil {
		s.onSend()
	}
	m, _ := entity.NewMessage(enum.RoleAssistant, "done")
	m.SetFinishReason(enum.FinishReasonStop)
	conv.Append(m)
	return nil
}

// A steer arriving during the turn's last provider call is never
// consumed — it stays in the inbox for the caller to recall or reroute;
// it must not leak into history.
func TestRunTurnLeavesUnconsumedSteer(t *testing.T) {
	d := Deps{
		Convs:  repoconversation.NewConversationRepository(t.TempDir()),
		Config: config.ConversationConfig{MaxTurnStep: 4},
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
	steers := &steerInbox{}
	svc := doneSvc{onSend: func() { steers.push("too late") }}
	if err := d.runTurn(t.Context(), "room", conv, EnvironmentBundle{}, nil, entity.Model{}, svc, nil, steers); err != nil {
		t.Fatal(err)
	}
	if len(conv.Messages()) != 2 {
		t.Fatalf("unconsumed steer entered history: %+v", conv.Messages())
	}
	if got, ok := steers.popLast(); !ok || got != "too late" {
		t.Fatalf("recall = %q,%v — the caller must get the line back", got, ok)
	}
}

// The inbox keeps FIFO on drain and LIFO on recall.
func TestSteerInboxFIFOAndRecall(t *testing.T) {
	in := &steerInbox{}
	in.push("first")
	in.push("second")
	if got, ok := in.popLast(); !ok || got != "second" {
		t.Fatalf("recall pops the newest: %q,%v", got, ok)
	}
	if drained := in.drain(); len(drained) != 1 || drained[0] != "first" {
		t.Fatalf("drain keeps FIFO of the remainder: %v", drained)
	}
	if _, ok := in.popLast(); ok {
		t.Fatal("empty inbox must report nothing to recall")
	}
}
