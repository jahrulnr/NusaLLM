package event

import (
	"github.com/jahrulnr/llm-base/infrastructure/domain/entity"
	"github.com/jahrulnr/llm-base/infrastructure/domain/enum"
	"github.com/jahrulnr/llm-base/infrastructure/domain/valueobject"
)

// StreamEvent is a domain event emitted while Send streams a response.
// Exactly one payload field is populated per Kind.
type StreamEvent struct {
	kind         enum.StreamEventKind
	delta        string
	toolCall     entity.ToolCall
	finishReason enum.FinishReason
	usage        valueobject.Usage
	err          error
}

func NewDeltaEvent(delta string) StreamEvent {
	return StreamEvent{kind: enum.StreamEventDelta, delta: delta}
}

func NewToolCallEvent(tc entity.ToolCall) StreamEvent {
	return StreamEvent{kind: enum.StreamEventToolCall, toolCall: tc}
}

func NewFinishedEvent(reason enum.FinishReason, usage valueobject.Usage) StreamEvent {
	return StreamEvent{kind: enum.StreamEventFinished, finishReason: reason, usage: usage}
}

func NewErrorEvent(err error) StreamEvent {
	return StreamEvent{kind: enum.StreamEventError, err: err}
}

func (e StreamEvent) Kind() enum.StreamEventKind      { return e.kind }
func (e StreamEvent) Delta() string                   { return e.delta }
func (e StreamEvent) ToolCall() entity.ToolCall       { return e.toolCall }
func (e StreamEvent) FinishReason() enum.FinishReason { return e.finishReason }
func (e StreamEvent) Usage() valueobject.Usage        { return e.usage }
func (e StreamEvent) Err() error                      { return e.err }

// LLMEventHandler consumes stream events. Slow handlers apply backpressure
// on the read loop; implementations must not invoke it concurrently.
type LLMEventHandler func(StreamEvent)
