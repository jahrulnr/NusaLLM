package service

import (
	"context"

	"github.com/jahrulnr/llm-base/infrastructure/domain/entity"
	"github.com/jahrulnr/llm-base/infrastructure/domain/valueobject"
)

// ToolEmitter streams incremental output while a call runs — chunks are a
// live preview for the frontend, NOT the persisted result. It is
// synchronous: slow consumers apply backpressure on the tool. nil means
// the caller isn't streaming; the tool may ignore it.
type ToolEmitter func(chunk string)

// Tool is one callable capability: its constructor builds and validates
// the ToolSchema it advertises, and Execute runs a model-originated call
// returning the canonical output — the caller wraps it in the
// untrusted-tool envelope via ToolCall.SetOutput, while emit delivers
// progressive previews. Execute failures are recorded AS the tool output
// so the model can see and react to them; only context cancellation
// aborts the surrounding turn. Tool selection (call name → tool) is
// application wiring; adapters never call this port.
type Tool interface {
	Schema() valueobject.ToolSchema
	Execute(ctx context.Context, call entity.ToolCall, emit ToolEmitter) (string, error)
}
