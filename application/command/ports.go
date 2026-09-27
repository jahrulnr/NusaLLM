package appcommand

import (
	"io"
	"io/fs"

	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

// Bindings is the mutable slash-client selection delivery owns — active
// conversation, route/model override, pending attachments, and REPL
// control flags. Implementations read/write the transport session.
type Bindings interface {
	ConversationID() string
	SetConversationID(string)
	Route() string
	SetRoute(string)
	ModelID() string
	SetModelID(string)
	Attachments() []valueobject.Attachment
	SetAttachments([]valueobject.Attachment)
	AppendAttachment(valueobject.Attachment)

	Interactive() bool
	SetExit(bool)
	RequestCancelTurn()
	CancelTurnActive() bool
	RequestClearScreen()
}

// WorkspacePort confines file paths to the client workspace. Implement
// with infrastructure/service/workspace.Root for CLI and web clients.
type WorkspacePort interface {
	Path() string
	FS() fs.FS
	ResolveAttachment(path string) (valueobject.Attachment, error)
	CreateFile(name string) (io.WriteCloser, error)
}

// OutputPort renders command text for a transport (terminal sanitize, etc.).
type OutputPort interface {
	Write(emitter dservice.CommandEmitter, format string, args ...any)
	SanitizeText(text string) string
}
