package commands

import (
	"context"

	appcommand "github.com/jahrulnr/nusallm/application/command"
	"github.com/jahrulnr/nusallm/infrastructure/delivery/cli/session"
	"github.com/jahrulnr/nusallm/infrastructure/delivery/cli/term"
	"github.com/jahrulnr/nusallm/infrastructure/domain/event"
	"github.com/jahrulnr/nusallm/infrastructure/domain/service"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
	svccommand "github.com/jahrulnr/nusallm/infrastructure/service/command"
)

// Registry is the terminal-bound slash-command registry.
type Registry = svccommand.Registry

// Info is one palette or /help row.
type Info = service.CommandCompletion

// AttachmentPath parses /attach argument text.
var AttachmentPath = svccommand.AttachmentPath

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

// Edit forwards to the session turn API for non-command callers.
func Edit(ctx context.Context, s *session.Session, convID, messageID, content string, onEvent event.TurnEventHandler) error {
	return s.Edit(ctx, convID, messageID, content, onEvent)
}
