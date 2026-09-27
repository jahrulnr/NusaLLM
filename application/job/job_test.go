package job

import (
	"context"
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/config"
	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/event"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
	repoconversation "github.com/jahrulnr/nusallm/infrastructure/repository/conversation"
)

// loopSvc asks for a tool on every provider call — the model that never
// stops requesting, which the step cap exists to catch.
type loopSvc struct{ calls int }

func (s *loopSvc) Send(_ context.Context, conv *aggregate.Conversation, _ *aggregate.Gateway, _ entity.Model, _ event.TurnEventHandler) error {
	s.calls++
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
		Convs:  repoconversation.NewConversationRepository(t.TempDir()),
		Config: config.ConversationConfig{MaxTurnStep: 2},
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
	err := d.runTurn(t.Context(), "room", conv, nil, entity.Model{}, &loopSvc{}, nil)
	if apperror.As(err).StatusCode() != 500 {
		t.Fatalf("step cap → %v, want 500", err)
	}
}

func TestPendingCallsFilter(t *testing.T) {
	m, _ := entity.NewMessage(enum.RoleAssistant, "calls")
	inject, _ := entity.NewToolCall("c1", "read", `{}`, enum.TooltypeInjection)
	answered, _ := entity.NewToolCall("c2", "echo", `{}`, enum.TooltypeOrigin)
	if err := answered.SetOutput("done"); err != nil {
		t.Fatal(err)
	}
	pending, _ := entity.NewToolCall("c3", "echo", `{}`, enum.TooltypeOrigin)
	m.AddToolCall(inject)
	m.AddToolCall(answered)
	m.AddToolCall(pending)
	got := pendingCalls(m)
	if len(got) != 1 || got[0].ID() != "c3" {
		t.Fatalf("want only the unanswered origin call, got %+v", got)
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
