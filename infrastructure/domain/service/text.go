package service

import (
	"context"

	"github.com/jahrulnr/llm-base/infrastructure/domain/aggregate"
	"github.com/jahrulnr/llm-base/infrastructure/domain/entity"
	"github.com/jahrulnr/llm-base/infrastructure/domain/event"
)

type TextModelService interface {
	// Send performs one completion attempt against the gateway the
	// implementation is bound to, appending the resulting assistant message
	// and usage onto conv. Model and gateway selection — including fallback
	// — belong to the application layer.
	//
	// In stream mode onEvent receives domain events as they arrive (deltas,
	// completed tool calls, finished/error). In non-stream mode the
	// implementation emits a single delta carrying the full content followed
	// by finished, so callers consume both modes uniformly. onEvent may be
	// nil.
	//
	// Failures return apperror.IAppError — the application routes on
	// StatusCode(): 429 tries another route, 401/403 marks a gateway dead,
	// 413 compacts rather than falls back. Message is frontend-safe;
	// upstream detail stays in Cause.
	Send(ctx context.Context, conv *aggregate.Conversation, model entity.Model, onEvent event.LLMEventHandler) error
}
