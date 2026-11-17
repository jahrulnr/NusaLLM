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

// A provider can answer finish_reason=tool_calls while every call it
// streamed had malformed/truncated arguments. Adapters intentionally
// drop incomplete calls, so the appended assistant message carries the
// finish reason but zero calls. That is not "the model is done": nothing
// executable followed, and the turn must fail loudly instead of
// persisting an empty assistant message.
type emptyToolCallSvc struct{}

func (s *emptyToolCallSvc) Send(_ context.Context, conv *aggregate.Conversation, _ *aggregate.Gateway, _ entity.Model, _ event.TurnEventHandler) error {
	m, _ := entity.NewMessage(enum.RoleAssistant, "")
	m.SetFinishReason(enum.FinishReasonToolCalls)
	conv.Append(m)
	return nil
}

func TestRunTurnRejectsToolCallFinishWithoutCalls(t *testing.T) {
	d := Deps{
		SummaryTool: mustSummaryTool(t),
		Convs:       repoconversation.NewConversationRepository(t.TempDir()),
		Config:      config.ConversationConfig{MaxTurnStep: 4},
	}
	conv := seedRoom(t, d.Convs, "room", "hi")
	if err := d.runTurn(t.Context(), "room", conv, EnvironmentBundle{}, nil, entity.Model{}, &emptyToolCallSvc{}, nil, nil); err == nil {
		t.Fatal("finish_reason=tool_calls with zero calls must fail the turn")
	}
}

// A canceled stream can append the text that already arrived before the
// caller stopped it. That partial reply must carry an interrupted
// finish marker — otherwise a reload cannot distinguish it from a
// complete answer whose provider omitted finish_reason.
type canceledPartialSvc struct{}

func (s *canceledPartialSvc) Send(_ context.Context, conv *aggregate.Conversation, _ *aggregate.Gateway, _ entity.Model, _ event.TurnEventHandler) error {
	m, _ := entity.NewMessage(enum.RoleAssistant, "partial answer")
	conv.Append(m)
	return apperror.New(408, "turn canceled", context.Canceled)
}

func TestRunTurnStampsInterruptedFinishOnPartialReply(t *testing.T) {
	d := Deps{
		SummaryTool: mustSummaryTool(t),
		Convs:       repoconversation.NewConversationRepository(t.TempDir()),
		Config:      config.ConversationConfig{MaxTurnStep: 4},
	}
	conv := seedRoom(t, d.Convs, "room", "hi")
	err := d.runTurn(t.Context(), "room", conv, EnvironmentBundle{}, nil, entity.Model{}, &canceledPartialSvc{}, nil, nil)
	if code := apperror.As(err).StatusCode(); code != 408 {
		t.Fatalf("canceled turn → %v (status %d), want 408", err, code)
	}
	if got := conv.Last().FinishReason(); got == "" {
		t.Fatal("persisted partial reply has no interrupted finish marker")
	}
}
