package service

import (
	"context"

	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/event"
)

type TextModelService interface {
	// Send performs one completion attempt against gw, appending the
	// resulting assistant message and usage onto conv. Implementations are
	// stateless translators — endpoint and credentials are read from gw per
	// call, so a re-Loaded gateway (rotated key, moved endpoint) takes
	// effect immediately. Model and gateway selection — including fallback
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
	Send(ctx context.Context, conv *aggregate.Conversation, gw *aggregate.Gateway, model entity.Model, onEvent event.TurnEventHandler) error
}

// CompactionSanitizer is the optional provider capability that owns
// prefix-bound replay artifacts. When compaction replaces the conversation
// prefix, signatures recorded against the old prefix cannot be replayed;
// adapters that emit such artifacts implement this method to return a tail
// with those artifacts removed. The input and its messages are not mutated;
// order and length are preserved so the caller can persist the rewritten
// tail alongside the checkpoint.
//
// Adapters whose replay artifacts remain valid across a prefix change do
// not implement this capability.
type CompactionSanitizer interface {
	SanitizeCompactedTail(tail []entity.Message) []entity.Message
}
