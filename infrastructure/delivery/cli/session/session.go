// Package session owns one terminal session's conversation state — the
// active conversation, route/model selection, pending attachments, and
// the turn executions against the use cases. It knows nothing about
// commands or the TUI; those boundaries bind to it.
package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	appconversation "github.com/jahrulnr/llm-base/application/conversation"
	appgateway "github.com/jahrulnr/llm-base/application/gateway"
	"github.com/jahrulnr/llm-base/infrastructure/delivery/cli/term"
	"github.com/jahrulnr/llm-base/infrastructure/domain/entity"
	"github.com/jahrulnr/llm-base/infrastructure/domain/event"
	"github.com/jahrulnr/llm-base/infrastructure/domain/service"
	"github.com/jahrulnr/llm-base/infrastructure/domain/valueobject"
	"github.com/jahrulnr/llm-base/infrastructure/service/workspace"
)

// Options wires a session to the conversation/gateway applications and
// its terminal workspace.
type Options struct {
	Conversations *appconversation.ConversationApp
	Gateways      *appgateway.GatewayApp
	Workspace     string
	Stderr        io.Writer
}

// Session owns the active conversation, route/model selection, and pending
// user-supplied attachments for one terminal session. The command and TUI
// boundaries mutate the exported fields directly — the session is the
// shared state they coordinate through.
type Session struct {
	Conversations *appconversation.ConversationApp
	Gateways      *appgateway.GatewayApp
	Workspace     *workspace.Root
	Stderr        io.Writer

	ConversationID string
	Route          string
	ModelID        string
	Attachments    []valueobject.Attachment
	Exit           bool
	Interactive    bool

	// CancelTurn interrupts the in-flight send/edit/compact — the TUI
	// binds it when a job starts and clears it when the job lands.
	// ClearQueued is a one-shot flag the /clear command raises for the
	// update loop to turn into tea.ClearScreen.
	CancelTurn  context.CancelFunc
	ClearQueued bool

	// Notify surfaces non-fatal warnings — the TUI overrides it to print
	// into scrollback; the default writes stderr.
	Notify func(string)
}

// New binds a session to the use cases and workspace root. Relative
// attachment paths are confined to that workspace.
func New(opts Options) (*Session, error) {
	if opts.Conversations == nil || opts.Gateways == nil {
		return nil, errors.New("conversation and gateway apps are required")
	}
	if opts.Workspace == "" {
		opts.Workspace = "."
	}
	workspace, err := workspace.Open(opts.Workspace)
	if err != nil {
		return nil, err
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}
	s := &Session{
		Conversations: opts.Conversations,
		Gateways:      opts.Gateways,
		Workspace:     workspace,
		Stderr:        opts.Stderr,
	}
	s.Notify = func(text string) {
		if !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		_, _ = term.Write(s.Stderr, text)
	}
	return s, nil
}

// Close releases the workspace root held for confined attachment reads.
func (s *Session) Close() error {
	return s.Workspace.Close()
}

// ResolveRoute picks the effective route and model for the next turn —
// explicit /route + /model selection, the conversation's stored binding,
// or an unambiguous auto-selection — and must run on the update loop:
// it writes s.Route/s.ModelID that the same loop renders, so the turn
// goroutine receives them as arguments rather than racing on the fields.
// An unambiguous setup selects itself and says so, rather than making
// the operator retype the only possible answer.
func (s *Session) ResolveRoute(ctx context.Context) (route, modelID string, err error) {
	route, modelID = s.Route, s.ModelID
	if s.ConversationID != "" {
		info, err := s.Conversations.Info(ctx, s.ConversationID)
		if err != nil {
			return "", "", err
		}
		if route == "" {
			route = info.Route
		}
		if modelID == "" {
			modelID = info.ModelID
		}
	}
	if route == "" {
		gateways, err := s.Gateways.List(ctx)
		if err != nil {
			return "", "", err
		}
		var ready []appgateway.GatewayInfo
		for _, g := range gateways {
			if !g.Broken {
				ready = append(ready, g)
			}
		}
		if len(ready) == 1 {
			route = ready[0].ID
			s.Route = route
			s.Notify(fmt.Sprintf("auto-selected route %s — the only configured gateway", route))
		}
	}
	if modelID == "" && route != "" {
		if gw, err := s.Gateways.Get(ctx, route); err == nil && !gw.Broken && len(gw.Models) == 1 {
			modelID = gw.Models[0].ID()
			s.ModelID = modelID
			s.Notify(fmt.Sprintf("auto-selected model %s — the only model on %s", modelID, route))
		}
	}
	if route == "" || modelID == "" {
		return "", "", errors.New("select a route and model with /route and /model before starting a conversation")
	}
	return route, modelID, nil
}

// Send runs one conversation turn. Modality warnings surface through
// Notify; persistence bookkeeping stays with the caller via SendResult.
func (s *Session) Send(ctx context.Context, text, route, modelID string, onEvent event.TurnEventHandler) (appconversation.SendResult, error) {
	if text == "" && len(s.Attachments) == 0 {
		return appconversation.SendResult{}, nil
	}
	gateway, err := s.Gateways.Get(ctx, route)
	if err != nil {
		return appconversation.SendResult{}, err
	}
	if gateway.Broken {
		return appconversation.SendResult{}, fmt.Errorf("gateway %q is broken and cannot be used", route)
	}
	if !HasModel(gateway.Models, modelID) {
		return appconversation.SendResult{}, fmt.Errorf("model %q is not in gateway %q; use /model to select one", modelID, route)
	}
	for _, model := range gateway.Models {
		if model.ID() != modelID {
			continue
		}
		for _, attachment := range s.Attachments {
			if !model.AcceptsInput(attachment.Modality()) {
				s.Notify(fmt.Sprintf("warning: %s attachment %s will be omitted; model %s does not accept it",
					attachment.Modality(), attachment.Name(), modelID))
			}
		}
		break
	}
	return s.Conversations.Send(ctx, s.ConversationID, route, modelID, text, s.Attachments, onEvent)
}

// Edit regenerates a conversation from a user message — a turn just like
// Send, so the caller supplies the event handler.
func (s *Session) Edit(ctx context.Context, convID, messageID, content string, onEvent event.TurnEventHandler) error {
	return s.Conversations.Edit(ctx, convID, messageID, content, onEvent)
}

// Write emits formatted output through the command emitter.
func (s *Session) Write(emitter service.CommandEmitter, format string, args ...any) {
	if emitter != nil {
		emitter(fmt.Sprintf(format, args...))
	}
}

// HasModel reports whether id is in the gateway's model catalog.
func HasModel(models []entity.Model, id string) bool {
	for _, model := range models {
		if model.ID() == id {
			return true
		}
	}
	return false
}
