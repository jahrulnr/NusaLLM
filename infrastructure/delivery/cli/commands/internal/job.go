package commandinternal

import (
	"context"

	"github.com/jahrulnr/nusallm/infrastructure/domain/event"
	"github.com/jahrulnr/nusallm/infrastructure/domain/service"
)

// JobFunc is a worker-bound command body — the transport runs it off its
// update loop with a command emitter for text output and a turn sink for
// streamed events; a nil sink means the caller is not streaming.
//
// It lives beside the commands rather than in the registry package because
// both sides name it: a job command returns it, and the registry dispatches
// it.
type JobFunc func(ctx context.Context, emit service.CommandEmitter, onEvent event.TurnEventHandler) error

// JobCommand is a slash command that runs as a worker job — /edit,
// /compact — rather than producing inline output. Job validates the
// arguments up front and returns the work as a closure, so a TUI runs it
// as a turn while synchronous transports run the same closure through
// Execute. One implementation serves both.
type JobCommand interface {
	service.SlashCommand
	Job(args ...string) (JobFunc, error)
}
