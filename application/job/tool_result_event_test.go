package job

// T-27 — the turn reports each call's verdict as data: one tool_result event
// per call, carrying the outcome and exit the adapter recorded, after the
// result is persisted.

import (
	"context"
	"strings"
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

// failingExec mimics the terminal adapter: it returns normally, with a failed
// verdict and the process status as data.
type failingExec struct {
	schema valueobject.ToolSchema
	calls  atomic.Int32
}

func (f *failingExec) Schema() valueobject.ToolSchema { return f.schema }
func (f *failingExec) Execute(context.Context, entity.ToolCall, dservice.ToolEmitter) (dservice.ToolResult, error) {
	f.calls.Add(1)
	return dservice.ToolResult{
		Text:    "boom\n(exit 3)",
		Outcome: enum.ToolOutcomeFailed,
		Exit:    &valueobject.ExitStatus{Code: 3},
	}, nil
}

// callThenFinish asks for the tool on its first step, then answers.
type callThenFinish struct{ calls atomic.Int32 }

func (s *callThenFinish) Send(_ context.Context, conv *aggregate.Conversation, _ *aggregate.Gateway, _ entity.Model, _ event.TurnEventHandler) error {
	if s.calls.Add(1) == 1 {
		m, _ := entity.NewMessage(enum.RoleAssistant, "running it")
		tc, err := entity.NewToolCall("call_1", "boom", "{}", enum.TooltypeOrigin)
		if err != nil {
			return err
		}
		_ = m.AddToolCall(tc)
		m.SetFinishReason(enum.FinishReasonToolCalls)
		conv.Append(m)
		return nil
	}
	m, _ := entity.NewMessage(enum.RoleAssistant, "done")
	m.SetFinishReason(enum.FinishReasonStop)
	conv.Append(m)
	return nil
}

func TestTurnEmitsToolResultVerdict(t *testing.T) {
	d := Deps{
		SummaryTool: mustSummaryTool(t),
		Convs:       repoconversation.NewConversationRepository(t.TempDir()),
		Config:      config.ConversationConfig{MaxTurnStep: 4},
	}
	schema, _ := valueobject.NewToolSchema("boom", "always fails", nil, true, enum.ToolPermissionAgent)
	tool := &failingExec{schema: schema}
	driver := EnvironmentBundle{Tools: map[string]dservice.Tool{"boom": tool}}
	conv := seedRoom(t, d.Convs, "room", "go")

	var results []event.TurnEvent
	onEvent := func(e event.TurnEvent) {
		if e.Kind() == enum.TurnEventToolResult {
			results = append(results, e)
		}
	}
	// The failed verdict is data, not a turn failure: the tool's own
	// failure is evidence the model reacts to on its next step.
	if err := d.runTurn(t.Context(), "room", conv, driver, nil, entity.Model{}, &callThenFinish{}, onEvent, nil); err != nil {
		t.Fatalf("turn error = %v, want the tool failure to stay inside the turn", err)
	}
	if tool.calls.Load() != 1 {
		t.Fatalf("tool calls = %d, want 1", tool.calls.Load())
	}
	if len(results) != 1 {
		t.Fatalf("tool_result events = %d, want exactly one per call", len(results))
	}
	call := results[0].ToolCall()
	if call.ID() != "call_1" || call.Outcome() != enum.ToolOutcomeFailed {
		t.Fatalf("event call = %s/%q, want call_1/failed", call.ID(), call.Outcome())
	}
	if exit := call.Exit(); exit == nil || exit.Code != 3 || exit.Signal != "" {
		t.Fatalf("event exit = %+v, want code 3", exit)
	}

	// The verdict is stored, not just streamed: a reload still knows.
	reloaded, err := d.Convs.Load(t.Context(), "room")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range reloaded.Messages() {
		for _, tc := range m.ToolCalls() {
			if tc.ID() != "call_1" {
				continue
			}
			if tc.Outcome() != enum.ToolOutcomeFailed || tc.Exit() == nil || tc.Exit().Code != 3 {
				t.Fatalf("reloaded call = %q/%+v, want failed/3", tc.Outcome(), tc.Exit())
			}
			// The text still carries the model-facing marker.
			if !strings.Contains(tc.Output(), "(exit 3)") {
				t.Fatalf("output lost the model-facing marker: %q", tc.Output())
			}
		}
	}
}
