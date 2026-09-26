package service

import "context"

// ToolEmitter streams incremental output while a call runs — chunks are a
// live preview for the frontend, NOT the persisted result. It is
// synchronous: slow consumers apply backpressure on the tool. nil means
// the caller isn't streaming; the tool may ignore it.
type CommandEmitter func(chunk string)

type CommandService interface {
	Execute(ctx context.Context, command string, emitter CommandEmitter, args ...string) error
}
