package event

import (
	"github.com/jahrulnr/llm-base/infrastructure/domain/entity"
	"github.com/jahrulnr/llm-base/infrastructure/domain/enum"
	"github.com/jahrulnr/llm-base/infrastructure/domain/valueobject"
)

// TurnEvent is a domain event emitted while Send streams a response.
// Exactly one payload field is populated per Kind.
type TurnEvent struct {
	kind         enum.TurnEventKind
	delta        string
	toolCall     entity.ToolCall
	finishReason enum.FinishReason
	usage        valueobject.Usage
	err          error
}

func NewDeltaEvent(delta string) TurnEvent {
	return TurnEvent{kind: enum.TurnEventDelta, delta: delta}
}

// NewReasoningEvent carries a thinking/reasoning chunk — visible scratchpad
// for live UI, kept out of the assistant's answer text.
func NewReasoningEvent(delta string) TurnEvent {
	return TurnEvent{kind: enum.TurnEventReasoning, delta: delta}
}

func NewToolCallEvent(tc entity.ToolCall) TurnEvent {
	return TurnEvent{kind: enum.TurnEventToolCall, toolCall: tc}
}

// NewToolOutputEvent carries a live chunk from a running tool — a preview
// for the frontend, not the persisted output. The call's id rides in
// ToolCall so parallel calls stay attributable.
func NewToolOutputEvent(call entity.ToolCall, chunk string) TurnEvent {
	return TurnEvent{kind: enum.TurnEventToolOutput, toolCall: call, delta: chunk}
}

func NewFinishedEvent(reason enum.FinishReason, usage valueobject.Usage) TurnEvent {
	return TurnEvent{kind: enum.TurnEventFinished, finishReason: reason, usage: usage}
}

func NewErrorEvent(err error) TurnEvent {
	return TurnEvent{kind: enum.TurnEventError, err: err}
}

func (e TurnEvent) Kind() enum.TurnEventKind        { return e.kind }
func (e TurnEvent) Delta() string                   { return e.delta }
func (e TurnEvent) ToolCall() entity.ToolCall       { return e.toolCall }
func (e TurnEvent) FinishReason() enum.FinishReason { return e.finishReason }
func (e TurnEvent) Usage() valueobject.Usage        { return e.usage }
func (e TurnEvent) Err() error                      { return e.err }

// TurnEventHandler consumes the events of one turn — one Send call.
// Slow handlers apply backpressure on the read loop; implementations
// must not invoke it concurrently.
type TurnEventHandler func(TurnEvent)
