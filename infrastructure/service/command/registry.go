// Package command implements slash commands shared by the REPL and other
// UIs. Each command is a service/command/<name> package with its own
// New* constructor — the registry only dispatches wired instances.
package command

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/jahrulnr/nusallm/infrastructure/domain/service"
	commandinternal "github.com/jahrulnr/nusallm/infrastructure/service/command/internal"
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
