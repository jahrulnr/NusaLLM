package service

import "context"

// CommandEmitter streams text from a command to its caller. It is
// synchronous, so a slow consumer applies backpressure; nil means the
// caller is not streaming.
type CommandEmitter func(chunk string)

// CommandService executes a named application command.
type CommandService interface {
	Execute(ctx context.Context, command string, emitter CommandEmitter, args ...string) error
}
