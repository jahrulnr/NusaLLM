package service

import (
	"context"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

// ToolEmitter streams incremental output while a call runs — chunks are a
// live preview for the frontend, NOT the persisted result. It is
// synchronous: slow consumers apply backpressure on the tool. nil means
// the caller isn't streaming; the tool may ignore it.
type ToolEmitter func(chunk string)

// ToolResult is the canonical output of one call: Text is raw evidence
// the caller records via ToolCall.SetOutput; provider adapters obtain the
// bounded untrusted-tool envelope from the entity getter.
// Attachments are stored media references (file://, https://, data:) the
// wire resolves at send time — the same rules as Message attachments.
// Attachments never consume the text output budget; whether they reach
// the model is decided per-request by the bound model's input modalities.
type ToolResult struct {
	Text        string
	Attachments []valueobject.Attachment
}

// Tool is one callable capability: its constructor builds and validates
// the ToolSchema it advertises, and Execute runs a model-originated call
// returning the canonical result — the caller stores Text raw via
// ToolCall.SetOutput, while emit delivers
// progressive previews. Execute failures are recorded AS the tool output
// so the model can see and react to them; only context cancellation
// aborts the surrounding turn. Tool selection (call name → tool) is
// application wiring; adapters never call this port.
type Tool interface {
	Schema() valueobject.ToolSchema
	Execute(ctx context.Context, call entity.ToolCall, emit ToolEmitter) (ToolResult, error)
}

// workspaceCtxKey scopes a turn's working directory to directory-aware
// tools (file sandbox, shell cwd) without widening the Execute signature.
type workspaceCtxKey struct{}
type conversationCtxKey struct{}

// WithConversationID identifies the owner of tool-produced files for this turn.
func WithConversationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, conversationCtxKey{}, id)
}

// ConversationID returns the owner of tool-produced files, if bound.
func ConversationID(ctx context.Context) string {
	id, _ := ctx.Value(conversationCtxKey{}).(string)
	return id
}

// WithWorkspaceDir returns a ctx carrying the absolute directory the
// turn's tools must work in. Empty dir leaves the ctx unchanged.
func WithWorkspaceDir(ctx context.Context, dir string) context.Context {
	if dir == "" {
		return ctx
	}
	return context.WithValue(ctx, workspaceCtxKey{}, dir)
}

// WorkspaceDir returns the turn-scoped working directory, "" when unset —
// tools fall back to their own configured root.
func WorkspaceDir(ctx context.Context) string {
	dir, _ := ctx.Value(workspaceCtxKey{}).(string)
	return dir
}

type webProviderCtxKey struct{}

// WebProvider carries the bound gateway's provider identity for the turn, so
// provider-capable tools (web search/fetch) can prefer the provider's own
// endpoint over the generic backend. Kind is the normalized provider name —
// the gateway id. Endpoint/APIKey are the gateway's resolved credentials so
// a tool never needs a second credential source for the same provider.
// Model is the bound model's wire id — required by provider-native paths
// whose search executes through the provider's own model surface (e.g. the
// OpenCode Responses web_search server tool).
type WebProvider struct {
	Kind     string
	Endpoint string
	APIKey   string
	Model    string
}

// WithWebProvider binds the turn's provider identity for web tools.
func WithWebProvider(ctx context.Context, p WebProvider) context.Context {
	if p.Kind == "" {
		return ctx
	}
	return context.WithValue(ctx, webProviderCtxKey{}, p)
}

// ProviderFrom returns the bound provider identity, if any.
func WebProviderFrom(ctx context.Context) (WebProvider, bool) {
	p, ok := ctx.Value(webProviderCtxKey{}).(WebProvider)
	return p, ok
}
