// Package commands is the terminal-bound slash-command surface: the
// registry, the session facade, and one delivery/cli/commands/<name>
// package per command with its own New* constructor. Parsing a REPL line
// and calling the application facade is delivery work, so it lives here
// rather than in infrastructure/service.
package commands

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode"

	commandinternal "github.com/jahrulnr/nusallm/infrastructure/delivery/cli/commands/internal"
	"github.com/jahrulnr/nusallm/infrastructure/domain/service"
)

// Registry dispatches wired slash commands by name.
type Registry struct {
	commands map[string]entry
}

type entry struct {
	usage    string
	desc     string
	exec     service.CommandService
	complete func(context.Context, string) []service.CommandCompletion
}

// NewRegistry registers each command under SlashCommand.Name(). Pass every
// command from the composition root — same pattern as tool wiring in main.
func NewRegistry(cmds ...service.SlashCommand) *Registry {
	r := &Registry{commands: make(map[string]entry, len(cmds))}
	for _, cmd := range cmds {
		e := entry{usage: cmd.Usage(), desc: cmd.Description(), exec: cmd}
		if comp, ok := cmd.(service.SlashCommandCompleter); ok {
			e.complete = func(ctx context.Context, arg string) []service.CommandCompletion {
				return completionsFromDomain(comp.Complete(ctx, arg))
			}
		}
		r.commands[strings.ToLower(cmd.Name())] = e
	}
	return r
}

// Describe collects metadata from wired commands (sorted by usage).
func Describe(cmds []service.SlashCommand) []service.CommandCompletion {
	infos := make([]service.CommandCompletion, 0, len(cmds))
	for _, cmd := range cmds {
		infos = append(infos, service.CommandCompletion{Usage: cmd.Usage(), Desc: cmd.Description()})
	}
	slices.SortFunc(infos, func(a, b service.CommandCompletion) int { return strings.Compare(a.Usage, b.Usage) })
	return infos
}

func completionsFromDomain(rows []service.CommandCompletion) []service.CommandCompletion {
	out := make([]service.CommandCompletion, len(rows))
	for i, row := range rows {
		out[i] = service.CommandCompletion{Usage: row.Usage, Desc: row.Desc, FillOnly: row.FillOnly}
	}
	return out
}

func (r *Registry) Execute(ctx context.Context, command string, emitter service.CommandEmitter, args ...string) error {
	command = strings.ToLower(strings.TrimPrefix(command, "/"))
	e, ok := r.commands[command]
	if !ok {
		return fmt.Errorf("unknown command %q; use /help", command)
	}
	return e.exec.Execute(ctx, command, emitter, args...)
}

// JobFunc is the contract a job command returns; the type lives with the
// commands so a leaf package can name it without importing this one.
type JobFunc = commandinternal.JobFunc

// Job resolves the worker-bound body of a command. isJob reports whether
// the named command runs as a job — true even when validation failed, in
// which case job is nil and err carries the reason. Inline and unknown
// commands return (nil, false, nil) for Execute to handle.
func (r *Registry) Job(command string, args ...string) (job JobFunc, isJob bool, err error) {
	e, ok := r.commands[strings.ToLower(strings.TrimPrefix(command, "/"))]
	if !ok {
		return nil, false, nil
	}
	jc, ok := e.exec.(commandinternal.JobCommand)
	if !ok {
		return nil, false, nil
	}
	job, err = jc.Job(args...)
	if job == nil && err == nil {
		err = fmt.Errorf("command %q returned no job", command)
	}
	return job, true, err
}

// Describe lists registered commands sorted by usage.
func (r *Registry) Describe() []service.CommandCompletion {
	infos := make([]service.CommandCompletion, 0, len(r.commands))
	for _, e := range r.commands {
		infos = append(infos, service.CommandCompletion{Usage: e.usage, Desc: e.desc})
	}
	slices.SortFunc(infos, func(a, b service.CommandCompletion) int { return strings.Compare(a.Usage, b.Usage) })
	return infos
}

func (r *Registry) Completable(command string) bool {
	e, ok := r.commands[strings.ToLower(strings.TrimPrefix(command, "/"))]
	return ok && e.complete != nil
}

func (r *Registry) Complete(ctx context.Context, buffer string) []service.CommandCompletion {
	if !strings.HasPrefix(buffer, "/") {
		return nil
	}
	i := strings.IndexFunc(buffer, unicode.IsSpace)
	if i < 0 || strings.ContainsRune(buffer, '\n') {
		return nil
	}
	e, ok := r.commands[strings.ToLower(strings.TrimPrefix(buffer[:i], "/"))]
	if !ok || e.complete == nil {
		return nil
	}
	rows := e.complete(ctx, strings.TrimLeftFunc(buffer[i:], unicode.IsSpace))
	if len(rows) == 0 {
		return nil
	}
	if len(rows) > commandinternal.MaxRows {
		rows = rows[:commandinternal.MaxRows]
	}
	return rows
}
