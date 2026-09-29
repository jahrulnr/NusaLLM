package appcommand

import (
	"context"
	"io"
	"io/fs"

	appgateway "github.com/jahrulnr/nusallm/application/gateway"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/event"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

// SlashSession is what slash-command handlers call: client bindings,
// workspace and output ports, and conversation/gateway use cases — without
// exposing *ConversationApp or *GatewayApp.
type SlashSession interface {
	Bindings
	WorkspacePort
	OutputPort

	ListConversations(ctx context.Context) ([]drepo.ConversationInfo, error)
	ConversationInfo(ctx context.Context, id string) (drepo.ConversationInfo, error)
	ConversationHistory(ctx context.Context, id string, start, end int) ([]entity.Message, error)
	DeleteConversation(ctx context.Context, id string) error
	RenameConversation(ctx context.Context, id, title string) error
	CompactConversation(ctx context.Context, id string) error
	EditConversation(ctx context.Context, convID, messageID, content string, onEvent event.TurnEventHandler) error
	ToolSchemas(ctx context.Context, convID string) ([]valueobject.ToolSchema, error)

	ListGateways(ctx context.Context) ([]appgateway.GatewayInfo, error)
	GetGateway(ctx context.Context, id string) (appgateway.GatewayInfo, error)
}

// Facade wires application use cases behind SlashSession.
type Facade struct {
	bindings  Bindings
	workspace WorkspacePort
	output    OutputPort
	convs     conversationAPI
	gateways  gatewayAPI
}

type conversationAPI interface {
	Info(ctx context.Context, id string) (drepo.ConversationInfo, error)
	List(ctx context.Context) ([]drepo.ConversationInfo, error)
	History(ctx context.Context, id string, start, end int) ([]entity.Message, error)
	Delete(ctx context.Context, id string) error
	Rename(ctx context.Context, id, title string) error
	Compact(ctx context.Context, id string) error
	Edit(ctx context.Context, convID, messageID, content string, onEvent event.TurnEventHandler) error
	ToolSchemas(ctx context.Context, convID string) ([]valueobject.ToolSchema, error)
}

type gatewayAPI interface {
	List(ctx context.Context) ([]appgateway.GatewayInfo, error)
	Get(ctx context.Context, id string) (appgateway.GatewayInfo, error)
}

// NewFacade binds delivery ports to conversation and gateway apps.
func NewFacade(convs conversationAPI, gateways gatewayAPI, bindings Bindings, workspace WorkspacePort, output OutputPort) SlashSession {
	return &Facade{
		bindings:  bindings,
		workspace: workspace,
		output:    output,
		convs:     convs,
		gateways:  gateways,
	}
}

func (f *Facade) ConversationID() string                    { return f.bindings.ConversationID() }
func (f *Facade) SetConversationID(id string)               { f.bindings.SetConversationID(id) }
func (f *Facade) Route() string                             { return f.bindings.Route() }
func (f *Facade) SetRoute(route string)                     { f.bindings.SetRoute(route) }
func (f *Facade) ModelID() string                           { return f.bindings.ModelID() }
func (f *Facade) SetModelID(id string)                      { f.bindings.SetModelID(id) }
func (f *Facade) Attachments() []valueobject.Attachment     { return f.bindings.Attachments() }
func (f *Facade) SetAttachments(a []valueobject.Attachment) { f.bindings.SetAttachments(a) }
func (f *Facade) AppendAttachment(a valueobject.Attachment) { f.bindings.AppendAttachment(a) }
func (f *Facade) Interactive() bool                         { return f.bindings.Interactive() }
func (f *Facade) SetExit(exit bool)                         { f.bindings.SetExit(exit) }
func (f *Facade) RequestCancelTurn()                        { f.bindings.RequestCancelTurn() }
func (f *Facade) CancelTurnActive() bool                    { return f.bindings.CancelTurnActive() }
func (f *Facade) RequestClearScreen()                       { f.bindings.RequestClearScreen() }

func (f *Facade) Path() string { return f.workspace.Path() }
func (f *Facade) FS() fs.FS    { return f.workspace.FS() }
func (f *Facade) ResolveAttachment(path string) (valueobject.Attachment, error) {
	return f.workspace.ResolveAttachment(path)
}
func (f *Facade) CreateFile(name string) (io.WriteCloser, error) { return f.workspace.CreateFile(name) }

func (f *Facade) Write(emitter dservice.CommandEmitter, format string, args ...any) {
	f.output.Write(emitter, format, args...)
}
func (f *Facade) SanitizeText(text string) string { return f.output.SanitizeText(text) }

func (f *Facade) ListConversations(ctx context.Context) ([]drepo.ConversationInfo, error) {
	return f.convs.List(ctx)
}
func (f *Facade) ConversationInfo(ctx context.Context, id string) (drepo.ConversationInfo, error) {
	return f.convs.Info(ctx, id)
}
func (f *Facade) ConversationHistory(ctx context.Context, id string, start, end int) ([]entity.Message, error) {
	return f.convs.History(ctx, id, start, end)
}
func (f *Facade) DeleteConversation(ctx context.Context, id string) error {
	return f.convs.Delete(ctx, id)
}
func (f *Facade) RenameConversation(ctx context.Context, id, title string) error {
	return f.convs.Rename(ctx, id, title)
}
func (f *Facade) CompactConversation(ctx context.Context, id string) error {
	return f.convs.Compact(ctx, id)
}
func (f *Facade) EditConversation(ctx context.Context, convID, messageID, content string, onEvent event.TurnEventHandler) error {
	return f.convs.Edit(ctx, convID, messageID, content, onEvent)
}
func (f *Facade) ToolSchemas(ctx context.Context, convID string) ([]valueobject.ToolSchema, error) {
	return f.convs.ToolSchemas(ctx, convID)
}

func (f *Facade) ListGateways(ctx context.Context) ([]appgateway.GatewayInfo, error) {
	return f.gateways.List(ctx)
}
func (f *Facade) GetGateway(ctx context.Context, id string) (appgateway.GatewayInfo, error) {
	return f.gateways.Get(ctx, id)
}
