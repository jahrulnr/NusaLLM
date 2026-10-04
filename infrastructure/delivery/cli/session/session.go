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

	appconversation "github.com/jahrulnr/nusallm/application/conversation"
	appgateway "github.com/jahrulnr/nusallm/application/gateway"
	appjob "github.com/jahrulnr/nusallm/application/job"
	"github.com/jahrulnr/nusallm/infrastructure/delivery/cli/term"
	"github.com/jahrulnr/nusallm/infrastructure/domain/event"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	"github.com/jahrulnr/nusallm/infrastructure/domain/service"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
	"github.com/jahrulnr/nusallm/infrastructure/service/workspace"
)

// Options wires a session to the conversation/gateway applications and
// its terminal workspace.
type Options struct {
	Conversations  *appconversation.ConversationApp
	Gateways       *appgateway.GatewayApp
	Workspace      string
	WorkspaceStore drepo.Workspace
	Stderr         io.Writer
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
	workspace, err := workspace.Open(opts.WorkspaceStore, opts.Workspace)
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

// EffectiveRouteModel returns route and model for display and budgeting:
// session overrides first, then the active conversation's stored binding.
func (s *Session) EffectiveRouteModel(ctx context.Context) (route, modelID string) {
	route, modelID = s.Route, s.ModelID
	if s.ConversationID == "" || s.Conversations == nil {
		return route, modelID
	}
	info, err := s.Conversations.Info(ctx, s.ConversationID)
	if err != nil {
		return route, modelID
	}
	if route == "" {
		route = info.Route
	}
	if modelID == "" {
		modelID = info.ModelID
	}
	return route, modelID
}

// ResolveRoute picks the effective route and model for the next turn —
// explicit /route + /model selection, the conversation's stored binding,
// or the gateway app's auto-selection — and must run on the update loop:
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
			if route != "" {
				s.Route = route
			}
		}
		if modelID == "" {
			modelID = info.ModelID
			if modelID != "" {
				s.ModelID = modelID
			}
		}
	}
	sel, err := s.Gateways.ResolveSelection(ctx, route, modelID)
	if err != nil {
		return "", "", err
	}
	if sel.AutoRoute {
		s.Route = sel.Route
		s.Notify(fmt.Sprintf("auto-selected route %s — the only configured gateway", sel.Route))
	}
	if sel.AutoModel {
		s.ModelID = sel.ModelID
		s.Notify(fmt.Sprintf("auto-selected model %s — the only model on %s", sel.ModelID, sel.Route))
	}
	if sel.Route == "" || sel.ModelID == "" {
		return "", "", errors.New("select a route and model with /route and /model before starting a conversation")
	}
	return sel.Route, sel.ModelID, nil
}

// Send runs one conversation turn. Modality warnings surface through
// Notify; persistence bookkeeping stays with the caller via SendResult.
// Route, model, and payload validation are the use case's call — this
// transport only warns where it can still help.
func (s *Session) Send(ctx context.Context, text, route, modelID string, onEvent event.TurnEventHandler) (appconversation.SendResult, error) {
	gateway, err := s.Gateways.Get(ctx, route)
	if err != nil {
		return appconversation.SendResult{}, err
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
	return s.Conversations.Send(ctx, s.ConversationID, route, modelID, text, s.Attachments, appjob.SendOptions{}, onEvent)
}

// Steer injects input into the bound conversation's running turn — it
// lands after the next tool result as an ordinary user message. Errors
// when no turn is running.
func (s *Session) Steer(ctx context.Context, text string) error {
	return s.Conversations.Steer(ctx, s.ConversationID, text)
}

// RecallSteer withdraws the newest unconsumed steering input — the
// composer restore path on Esc.
func (s *Session) RecallSteer() (string, bool) {
	return s.Conversations.RecallSteer(s.ConversationID)
}

// Write emits formatted output through the command emitter.
func (s *Session) Write(emitter service.CommandEmitter, format string, args ...any) {
	if emitter != nil {
		emitter(fmt.Sprintf(format, args...))
	}
}
