package event

import (
	"errors"
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

func TestTurnEventKinds(t *testing.T) {
	d := NewDeltaEvent("chunk")
	if d.Kind() != enum.TurnEventDelta || d.Delta() != "chunk" {
		t.Fatalf("delta event malformed: %+v", d)
	}
	if d.ToolCall().ID() != "" || d.Err() != nil {
		t.Fatal("delta event must carry only delta payload")
	}

	tc, _ := entity.NewToolCall("c", "n", "{}", enum.TooltypeOrigin)
	te := NewToolCallEvent(tc)
	if te.Kind() != enum.TurnEventToolCall || te.ToolCall().ID() != "c" {
		t.Fatalf("tool call event malformed: %+v", te)
	}

	u, _ := valueobject.NewUsage(1, 2, 3, 4)
	fe := NewFinishedEvent(enum.FinishReasonStop, u)
	if fe.Kind() != enum.TurnEventFinished || fe.FinishReason() != enum.FinishReasonStop ||
		fe.Usage().Total() != 10 {
		t.Fatalf("finished event malformed: %+v", fe)
	}

	sent := errors.New("boom")
	ee := NewErrorEvent(sent)
	if ee.Kind() != enum.TurnEventError || !errors.Is(ee.Err(), sent) {
		t.Fatalf("error event malformed: %+v", ee)
	}
}
