package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jahrulnr/llm-base/infrastructure/domain/entity"
	"github.com/jahrulnr/llm-base/infrastructure/domain/service"
)

type commandRegistry struct {
	commands map[string]service.CommandService
}

type commandFunc func(context.Context, service.CommandEmitter, string) error

func (f commandFunc) Execute(ctx context.Context, _ string, emitter service.CommandEmitter, args ...string) error {
	var raw string
	if len(args) > 0 {
		raw = args[0]
	}
	return f(ctx, emitter, raw)
}

func (r *REPL) commandRegistry() *commandRegistry {
	return &commandRegistry{commands: map[string]service.CommandService{
		"attach":  commandFunc(r.commandAttach),
		"edit":    commandFunc(r.commandEdit),
		"exit":    commandFunc(r.commandExit),
		"help":    commandFunc(r.commandHelp),
		"history": commandFunc(r.commandHistory),
		"list":    commandFunc(r.commandList),
		"model":   commandFunc(r.commandModel),
		"new":     commandFunc(r.commandNew),
		"quit":    commandFunc(r.commandExit),
		"resume":  commandFunc(r.commandResume),
		"route":   commandFunc(r.commandRoute),
	}}
}

func (r *commandRegistry) Execute(ctx context.Context, command string, emitter service.CommandEmitter, args ...string) error {
	command = strings.ToLower(strings.TrimPrefix(command, "/"))
	handler, ok := r.commands[command]
	if !ok {
		return fmt.Errorf("unknown command %q; use /help", command)
	}
	return handler.Execute(ctx, command, emitter, args...)
}

func (r *commandRegistry) Suggestions() []string {
	suggestions := make([]string, 0, len(r.commands))
	for command := range r.commands {
		suggestions = append(suggestions, "/"+command)
	}
	slices.Sort(suggestions)
	return suggestions
}

type commandCompleter struct {
	commands service.AutoCompleteService
}

func (c commandCompleter) Do(line []rune, pos int) ([][]rune, int) {
	if pos < 0 || pos > len(line) {
		return nil, 0
	}
	prefix := string(line[:pos])
	if !strings.HasPrefix(prefix, "/") || strings.IndexFunc(prefix, unicode.IsSpace) >= 0 {
		return nil, 0
	}
	var candidates [][]rune
	for _, suggestion := range c.commands.Suggestions() {
		if strings.HasPrefix(suggestion, prefix) {
			candidates = append(candidates, []rune(strings.TrimPrefix(suggestion, prefix)))
		}
	}
	if len(candidates) == 0 {
		return nil, 0
	}
	return candidates, pos
}

func (r *REPL) commandHelp(_ context.Context, emitter service.CommandEmitter, _ string) error {
	r.write(emitter, "Commands:\n")
	r.write(emitter, "  /new                 start a conversation\n")
	r.write(emitter, "  /resume <id>         resume a saved conversation\n")
	r.write(emitter, "  /list                list saved conversations\n")
	r.write(emitter, "  /history [count]     show recent messages and ids\n")
	r.write(emitter, "  /route [gateway-id]  list or select a gateway\n")
	r.write(emitter, "  /model [model-id]    list or select a model on the route\n")
	r.write(emitter, "  /attach <path>       attach a workspace file to the next turn\n")
	r.write(emitter, "  /edit <message-id> <text>  replace and regenerate on the saved route/model\n")
	r.write(emitter, "  /exit, /quit         leave the REPL\n")
	r.write(emitter, "Ctrl+C cancels the active turn; Ctrl+D exits at the prompt.\n")
	r.write(emitter, "Agent tool calls run automatically; run_terminal is unsandboxed. Review command activity.\n")
	return nil
}

func (r *REPL) commandExit(_ context.Context, emitter service.CommandEmitter, _ string) error {
	r.exit = true
	r.write(emitter, "goodbye\n")
	return nil
}

func (r *REPL) commandNew(ctx context.Context, emitter service.CommandEmitter, args string) error {
	if strings.TrimSpace(args) != "" {
		return errors.New("usage: /new")
	}
	if r.conversationID != "" {
		info, err := r.conversations.Info(ctx, r.conversationID)
		if err != nil {
			return err
		}
		if r.route == "" {
			r.route = info.Route
		}
		if r.modelID == "" {
			if r.route == info.Route {
				r.modelID = info.ModelID
			} else if gateway, err := r.gateways.Get(ctx, r.route); err == nil && !gateway.Broken && hasModel(gateway.Models, info.ModelID) {
				r.modelID = info.ModelID
			}
		}
	}
	r.conversationID = ""
	r.write(emitter, "new conversation; route=%q model=%q\n", r.route, r.modelID)
	return nil
}

func (r *REPL) commandResume(ctx context.Context, emitter service.CommandEmitter, args string) error {
	id := strings.TrimSpace(args)
	if id == "" || strings.IndexFunc(id, unicode.IsSpace) >= 0 {
		return errors.New("usage: /resume <conversation-id>")
	}
	conv, err := r.conversations.Info(ctx, id)
	if err != nil {
		return err
	}
	r.conversationID = id
	r.route, r.modelID = "", "" // use the binding stored with the conversation
	r.write(emitter, "resumed %s (route=%s model=%s)\n", id, conv.Route, conv.ModelID)
	return nil
}

func (r *REPL) commandList(ctx context.Context, emitter service.CommandEmitter, args string) error {
	if strings.TrimSpace(args) != "" {
		return errors.New("usage: /list")
	}
	conversations, err := r.conversations.List(ctx)
	if err != nil {
		return err
	}
	if len(conversations) == 0 {
		r.write(emitter, "no saved conversations\n")
		return nil
	}
	for _, conv := range conversations {
		marker := " "
		if conv.ID == r.conversationID {
			marker = "*"
		}
		r.write(emitter, "%s %s route=%s model=%s messages=%d", marker, conv.ID, conv.Route, conv.ModelID, conv.MessageCount)
		if conv.Title != "" {
			r.write(emitter, " title=%q", conv.Title)
		}
		if !conv.UpdatedAt.IsZero() {
			r.write(emitter, " updated=%s", conv.UpdatedAt.Local().Format(time.RFC3339))
		}
		r.write(emitter, "\n")
	}
	return nil
}

func (r *REPL) commandHistory(ctx context.Context, emitter service.CommandEmitter, args string) error {
	if r.conversationID == "" {
		return errors.New("no active conversation; use /resume <id>")
	}
	limit := 20
	if raw := strings.TrimSpace(args); raw != "" {
		if strings.IndexFunc(raw, unicode.IsSpace) >= 0 {
			return errors.New("usage: /history [count]")
		}
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 200 {
			return errors.New("history count must be between 1 and 200")
		}
		limit = parsed
	}
	messages, err := r.conversations.History(ctx, r.conversationID, -limit, 0)
	if err != nil {
		return err
	}
	for _, message := range messages {
		r.write(emitter, "%s %s", message.ID(), message.Role())
		if message.Content() != "" {
			r.write(emitter, ": %s", message.Content())
		}
		r.write(emitter, "\n")
		for _, attachment := range message.Attachments() {
			r.write(emitter, "  [attachment %s %s]\n", attachment.Name(), attachment.Modality())
		}
		for _, call := range message.ToolCalls() {
			r.write(emitter, "  [tool %s %s] %s\n", call.Name(), call.ID(), call.Input())
			if call.Output() != "" {
				r.write(emitter, "  [tool result %s] %s\n", call.ID(), call.Output())
			}
			for _, attachment := range call.OutputAttachments() {
				r.write(emitter, "  [tool attachment %s %s]\n", attachment.Name(), attachment.Modality())
			}
		}
	}
	return nil
}

func (r *REPL) commandRoute(ctx context.Context, emitter service.CommandEmitter, args string) error {
	id := strings.TrimSpace(args)
	if id == "" {
		gateways, err := r.gateways.List(ctx)
		if err != nil {
			return err
		}
		if len(gateways) == 0 {
			r.write(emitter, "no configured gateways\n")
			return nil
		}
		selected := r.route
		if selected == "" && r.conversationID != "" {
			info, err := r.conversations.Info(ctx, r.conversationID)
			if err != nil {
				return err
			}
			selected = info.Route
		}
		for _, gateway := range gateways {
			marker := " "
			if gateway.ID == selected {
				marker = "*"
			}
			key := "no-key"
			if gateway.KeySet {
				key = "key-set"
			}
			broken := ""
			if gateway.Broken {
				broken = " broken"
			}
			r.write(emitter, "%s %s %s style=%s models=%d %s%s\n", marker, gateway.ID, gateway.Name, gateway.Style, len(gateway.Models), key, broken)
		}
		return nil
	}
	if strings.IndexFunc(id, unicode.IsSpace) >= 0 {
		return errors.New("usage: /route [gateway-id]")
	}
	gateway, err := r.gateways.Get(ctx, id)
	if err != nil {
		return err
	}
	if gateway.Broken {
		return fmt.Errorf("gateway %q is broken and cannot be selected", id)
	}
	r.route = gateway.ID
	if r.modelID != "" && !hasModel(gateway.Models, r.modelID) {
		r.modelID = ""
	}
	r.write(emitter, "route set to %s (%s)\n", gateway.ID, gateway.Name)
	return nil
}

func (r *REPL) commandModel(ctx context.Context, emitter service.CommandEmitter, args string) error {
	route, selected := r.route, r.modelID
	if r.conversationID != "" && (route == "" || selected == "") {
		info, err := r.conversations.Info(ctx, r.conversationID)
		if err != nil {
			return err
		}
		if route == "" {
			route = info.Route
		}
		if selected == "" {
			selected = info.ModelID
		}
	}
	if route == "" {
		return errors.New("select a route with /route <gateway-id> first")
	}
	gateway, err := r.gateways.Get(ctx, route)
	if err != nil {
		return err
	}
	if gateway.Broken {
		return fmt.Errorf("gateway %q is broken and cannot be used", route)
	}
	id := strings.TrimSpace(args)
	if id == "" {
		if len(gateway.Models) == 0 {
			r.write(emitter, "gateway %s has no models\n", route)
			return nil
		}
		for _, model := range gateway.Models {
			marker := " "
			if model.ID() == selected {
				marker = "*"
			}
			r.write(emitter, "%s %s\n", marker, model.ID())
		}
		return nil
	}
	if strings.IndexFunc(id, unicode.IsSpace) >= 0 {
		return errors.New("usage: /model [model-id]")
	}
	if !hasModel(gateway.Models, id) {
		return fmt.Errorf("model %q is not in gateway %q; use /model to list", id, route)
	}
	r.modelID = id
	r.write(emitter, "model set to %s on %s\n", id, route)
	return nil
}

func (r *REPL) commandAttach(_ context.Context, emitter service.CommandEmitter, args string) error {
	path, err := attachmentPath(args)
	if err != nil {
		return err
	}
	attachment, err := resolveAttachment(r.workspace, r.workspacePath, path)
	if err != nil {
		return err
	}
	r.attachments = append(r.attachments, attachment)
	r.write(emitter, "attached %s (%s) for the next turn\n", attachment.Name(), attachment.Modality())
	return nil
}

func (r *REPL) commandEdit(ctx context.Context, emitter service.CommandEmitter, args string) error {
	if r.conversationID == "" {
		return errors.New("no active conversation; use /resume <id>")
	}
	messageID, content, ok := splitFirst(args)
	if !ok {
		return errors.New("usage: /edit <message-id> <replacement text>")
	}
	turnCtx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()
	renderer := newTurnRenderer(r.stdout)
	if err := r.conversations.Edit(turnCtx, r.conversationID, messageID, content, renderer.Handle); err != nil {
		return err
	}
	if err := renderer.Err(); err != nil {
		return err
	}
	return nil
}

func hasModel(models []entity.Model, id string) bool {
	for _, model := range models {
		if model.ID() == id {
			return true
		}
	}
	return false
}

func attachmentPath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("usage: /attach <workspace-relative-path>")
	}
	if raw[0] == '"' {
		path, err := strconv.Unquote(raw)
		if err != nil {
			return "", fmt.Errorf("invalid quoted attachment path: %w", err)
		}
		raw = path
	}
	return raw, nil
}

func splitFirst(raw string) (first, rest string, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", false
	}
	boundary := strings.IndexFunc(raw, unicode.IsSpace)
	if boundary < 0 {
		return raw, "", true
	}
	return raw[:boundary], strings.TrimSpace(raw[boundary:]), true
}
