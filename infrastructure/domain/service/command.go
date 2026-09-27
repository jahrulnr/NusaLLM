package service

import "context"

// CommandEmitter streams text from a command to its caller. It is
// synchronous, so a slow consumer applies backpressure; nil means the
// caller is not streaming.
type CommandEmitter func(chunk string)

// CommandService executes a named application command. Callers pass the text
// after the command name as args: the REPL registry uses a single args[0]
// holding the remainder of the input line; handlers parse that string (for
// example valueobject.NewCommand(raw).SplitFirst for id + tail).
type CommandService interface {
	Execute(ctx context.Context, command string, emitter CommandEmitter, args ...string) error
}

// CommandCompletion is one palette row for a slash command argument.
type CommandCompletion struct {
	Usage    string
	Desc     string
	FillOnly bool
}

// SlashCommand is one user-facing /command — identity and help text live
// on the implementation, like ToolSchema on service.Tool. Wiring selects
// commands by Name(); the registry does not own a separate catalog.
// Handlers receive application/command.SlashSession (facade), not
// concrete conversation or gateway apps.
type SlashCommand interface {
	CommandService
	Name() string
	Usage() string
	Description() string
}

// SlashCommandCompleter is optional argument completion for a SlashCommand.
type SlashCommandCompleter interface {
	Complete(ctx context.Context, arg string) []CommandCompletion
}
