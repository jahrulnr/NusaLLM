package commands

import (
	appcommand "github.com/jahrulnr/nusallm/application/command"
	"github.com/jahrulnr/nusallm/infrastructure/delivery/cli/session"
	"github.com/jahrulnr/nusallm/infrastructure/delivery/cli/term"
	"github.com/jahrulnr/nusallm/infrastructure/domain/service"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
	svccommand "github.com/jahrulnr/nusallm/infrastructure/service/command"
)

// Registry is the terminal-bound slash-command registry.
type Registry = svccommand.Registry

// Info is one palette or /help row.
type Info = service.CommandCompletion

// JobFunc is a worker-bound command body the registry resolved — the
// update loop runs it as a turn with live event streaming.
type JobFunc = svccommand.JobFunc

// SteerInfo advertises the terminal-local /steer command — the REPL
// input path handles it, not the service registry, so palette and /help
// need this manual row for discoverability.
var SteerInfo = service.CommandCompletion{
	Usage: "/steer <message>",
	Desc:  "inject input into the running turn after the next tool result",
}

type sessionBindings struct{ s *session.Session }

func (b *sessionBindings) ConversationID() string                    { return b.s.ConversationID }
func (b *sessionBindings) SetConversationID(id string)               { b.s.ConversationID = id }
func (b *sessionBindings) Route() string                             { return b.s.Route }
func (b *sessionBindings) SetRoute(route string)                     { b.s.Route = route }
func (b *sessionBindings) ModelID() string                           { return b.s.ModelID }
func (b *sessionBindings) SetModelID(id string)                      { b.s.ModelID = id }
func (b *sessionBindings) Attachments() []valueobject.Attachment     { return b.s.Attachments }
func (b *sessionBindings) SetAttachments(a []valueobject.Attachment) { b.s.Attachments = a }
func (b *sessionBindings) AppendAttachment(a valueobject.Attachment) {
	b.s.Attachments = append(b.s.Attachments, a)
}
func (b *sessionBindings) Interactive() bool { return b.s.Interactive }
func (b *sessionBindings) SetExit(exit bool) { b.s.Exit = exit }
func (b *sessionBindings) RequestCancelTurn() {
	if b.s.CancelTurn != nil {
		b.s.CancelTurn()
	}
}
func (b *sessionBindings) CancelTurnActive() bool { return b.s.CancelTurn != nil }
func (b *sessionBindings) RequestClearScreen()    { b.s.ClearQueued = true }

type sessionOutput struct{ s *session.Session }

func (o *sessionOutput) Write(emitter service.CommandEmitter, format string, args ...any) {
	o.s.Write(emitter, format, args...)
}
func (o *sessionOutput) SanitizeText(text string) string { return term.Sanitize(text) }

// Session builds the slash-session facade for one terminal session.
func Session(s *session.Session) appcommand.SlashSession {
	return appcommand.NewFacade(
		s.Conversations,
		s.Gateways,
		&sessionBindings{s},
		s.Workspace,
		&sessionOutput{s},
	)
}
