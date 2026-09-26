// Package commands is the slash-command boundary: the registry maps
// command names to handlers that mutate the session through its exported
// state and report through the emitter. It knows nothing about the TUI —
// the model calls Execute, the palette reads Describe and Complete.
package commands

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/atotto/clipboard"
	"github.com/jahrulnr/llm-base/infrastructure/delivery/cli/attach"
	"github.com/jahrulnr/llm-base/infrastructure/delivery/cli/session"
	"github.com/jahrulnr/llm-base/infrastructure/delivery/cli/term"
	"github.com/jahrulnr/llm-base/infrastructure/domain/entity"
	"github.com/jahrulnr/llm-base/infrastructure/domain/enum"
	"github.com/jahrulnr/llm-base/infrastructure/domain/service"
)

// Registry binds every slash command's handler to the session it mutates.
type Registry struct {
	s        *session.Session
	commands map[string]commandEntry
}

// commandEntry bundles a command's handler with its palette/help row, so
// usage and description stay a single source for both. complete is an
// optional argument completer — commands whose argument names a stored
// object (a conversation id) get a picker row per candidate.
type commandEntry struct {
	exec     service.CommandService
	usage    string
	desc     string
	complete func(context.Context, string) []Info
}

type commandFunc func(context.Context, service.CommandEmitter, string) error

func (f commandFunc) Execute(ctx context.Context, _ string, emitter service.CommandEmitter, args ...string) error {
	var raw string
	if len(args) > 0 {
		raw = args[0]
	}
	return f(ctx, emitter, raw)
}

// New builds the registry for one session.
func New(s *session.Session) *Registry {
	r := &Registry{s: s}
	r.commands = map[string]commandEntry{
		"attach":  {exec: commandFunc(r.commandAttach), usage: "/attach [path]", desc: "attach a workspace file to the next turn", complete: r.completeAttachPaths},
		"cancel":  {exec: commandFunc(r.commandCancel), usage: "/cancel", desc: "interrupt the active turn"},
		"clear":   {exec: commandFunc(r.commandClear), usage: "/clear", desc: "clear the terminal screen"},
		"compact": {exec: commandFunc(r.commandCompact), usage: "/compact", desc: "fold history into a checkpoint now"},
		"copy":    {exec: commandFunc(r.commandCopy), usage: "/copy", desc: "copy the last answer to the clipboard"},
		"delete":  {exec: commandFunc(r.commandDelete), usage: "/delete [id]", desc: "pick or delete a conversation", complete: r.completeConversations("delete")},
		"detach":  {exec: commandFunc(r.commandDetach), usage: "/detach", desc: "drop pending attachments"},
		"diff":    {exec: commandFunc(r.commandDiff), usage: "/diff", desc: "show the workspace git diff"},
		"edit":    {exec: commandFunc(r.commandEdit), usage: "/edit <id> <text>", desc: "replace a message and regenerate", complete: r.completeMessages("edit")},
		"exit":    {exec: commandFunc(r.commandExit), usage: "/exit", desc: "leave the REPL"},
		"export":  {exec: commandFunc(r.commandExport), usage: "/export <file>", desc: "write the transcript to a workspace file"},
		"help":    {exec: commandFunc(r.commandHelp), usage: "/help", desc: "show this command list"},
		"history": {exec: commandFunc(r.commandHistory), usage: "/history [count]", desc: "show recent messages and ids"},
		"list":    {exec: commandFunc(r.commandList), usage: "/list", desc: "list saved conversations"},
		"model":   {exec: commandFunc(r.commandModel), usage: "/model [model-id]", desc: "list or select a model on the route", complete: r.completeModels("model")},
		"new":     {exec: commandFunc(r.commandNew), usage: "/new", desc: "start a fresh conversation"},
		"pwd":     {exec: commandFunc(r.commandPwd), usage: "/pwd", desc: "print the workspace path"},
		"quit":    {exec: commandFunc(r.commandExit), usage: "/quit", desc: "alias for /exit"},
		"resume":  {exec: commandFunc(r.commandResume), usage: "/resume [id]", desc: "resume a saved conversation and show recent messages", complete: r.completeConversations("resume")},
		"route":   {exec: commandFunc(r.commandRoute), usage: "/route [gateway-id]", desc: "list or select a gateway", complete: r.completeGateways("route")},
		"status":  {exec: commandFunc(r.commandStatus), usage: "/status", desc: "show the session state"},
		"title":   {exec: commandFunc(r.commandTitle), usage: "/title <text>", desc: "rename the conversation"},
		"tools":   {exec: commandFunc(r.commandTools), usage: "/tools", desc: "list the agent's tools"},
		"usage":   {exec: commandFunc(r.commandUsage), usage: "/usage", desc: "show token accounting"},
	}
	return r
}

func (r *Registry) Execute(ctx context.Context, command string, emitter service.CommandEmitter, args ...string) error {
	command = strings.ToLower(strings.TrimPrefix(command, "/"))
	entry, ok := r.commands[command]
	if !ok {
		return fmt.Errorf("unknown command %q; use /help", command)
	}
	return entry.exec.Execute(ctx, command, emitter, args...)
}

// Info is one row of the palette and /help output. FillOnly marks an
// argument row where Enter completes the line instead of running it —
// "/edit m_1 " waits for replacement text, "/attach dir/" keeps listing.
type Info struct {
	Usage    string
	Desc     string
	FillOnly bool
}

// Describe lists commands sorted by usage — palette and /help share it.
func (r *Registry) Describe() []Info {
	infos := make([]Info, 0, len(r.commands))
	for _, entry := range r.commands {
		infos = append(infos, Info{Usage: entry.usage, Desc: entry.desc})
	}
	slices.SortFunc(infos, func(a, b Info) int { return strings.Compare(a.Usage, b.Usage) })
	return infos
}

// completeMaxRows caps a completer's rows — the palette renders a scroll
// window, so materializing hundreds of candidates per keystroke is waste.
const completeMaxRows = 50

// Completable reports whether a command fills its argument from a picker
// — bare invocation on a TTY parks at the argument position.
func (r *Registry) Completable(command string) bool {
	entry, ok := r.commands[strings.ToLower(strings.TrimPrefix(command, "/"))]
	return ok && entry.complete != nil
}

// Complete returns argument-completion rows for buffers past the command
// token — "/resume tw" completes conversation ids. Nil for a bare token
// (command completion is Describe's job), a multi-line buffer, or a
// command with no completer. The arg keeps its trailing whitespace —
// "/edit m_1 " means the id is done and the row list should close.
func (r *Registry) Complete(ctx context.Context, buffer string) []Info {
	if !strings.HasPrefix(buffer, "/") {
		return nil
	}
	i := strings.IndexFunc(buffer, unicode.IsSpace)
	if i < 0 || strings.ContainsRune(buffer, '\n') {
		return nil
	}
	entry, ok := r.commands[strings.ToLower(strings.TrimPrefix(buffer[:i], "/"))]
	if !ok || entry.complete == nil {
		return nil
	}
	rows := entry.complete(ctx, strings.TrimLeftFunc(buffer[i:], unicode.IsSpace))
	if len(rows) > completeMaxRows {
		rows = rows[:completeMaxRows]
	}
	return rows
}

// completeConversations builds the conversation-id completer shared by
// /resume and /delete: saved conversations most recent first, filtered by
// the typed prefix against id and title.
func (r *Registry) completeConversations(cmd string) func(context.Context, string) []Info {
	return func(ctx context.Context, arg string) []Info {
		if strings.IndexFunc(arg, unicode.IsSpace) >= 0 {
			return nil
		}
		conversations, err := r.s.Conversations.List(ctx)
		if err != nil {
			return nil
		}
		arg = strings.ToLower(arg)
		var rows []Info
		for _, conv := range conversations {
			if arg != "" &&
				!strings.Contains(strings.ToLower(conv.ID), arg) &&
				!strings.Contains(strings.ToLower(conv.Title), arg) {
				continue
			}
			title := term.Sanitize(conv.Title)
			if title == "" {
				title = "untitled"
			}
			if runes := []rune(title); len(runes) > 48 {
				title = string(runes[:48]) + "…"
			}
			desc := fmt.Sprintf("%s · %d msgs", title, conv.MessageCount)
			if !conv.UpdatedAt.IsZero() {
				desc += " · " + conv.UpdatedAt.Local().Format("Jan 02 15:04")
			}
			rows = append(rows, Info{Usage: "/" + cmd + " " + conv.ID, Desc: desc})
		}
		return rows
	}
}

// completeGateways builds the /route completer: configured gateways,
// filtered by id and name, flagged for key presence and broken state.
func (r *Registry) completeGateways(cmd string) func(context.Context, string) []Info {
	return func(ctx context.Context, arg string) []Info {
		if strings.IndexFunc(arg, unicode.IsSpace) >= 0 {
			return nil
		}
		gateways, err := r.s.Gateways.List(ctx)
		if err != nil {
			return nil
		}
		arg = strings.ToLower(arg)
		var rows []Info
		for _, gateway := range gateways {
			if arg != "" &&
				!strings.Contains(strings.ToLower(gateway.ID), arg) &&
				!strings.Contains(strings.ToLower(gateway.Name), arg) {
				continue
			}
			desc := fmt.Sprintf("%s · %s · %d models", gateway.Name, gateway.Style, len(gateway.Models))
			if !gateway.KeySet {
				desc += " · no-key"
			}
			if gateway.Broken {
				desc += " · broken"
			}
			if gateway.ID == r.s.Route {
				desc = "* " + desc
			}
			rows = append(rows, Info{Usage: "/" + cmd + " " + gateway.ID, Desc: desc})
		}
		return rows
	}
}

// completeModels builds the /model completer: models on the active route
// (explicit, else the conversation's stored binding), filtered by id.
func (r *Registry) completeModels(cmd string) func(context.Context, string) []Info {
	return func(ctx context.Context, arg string) []Info {
		if strings.IndexFunc(arg, unicode.IsSpace) >= 0 {
			return nil
		}
		route := r.s.Route
		if route == "" && r.s.ConversationID != "" {
			info, err := r.s.Conversations.Info(ctx, r.s.ConversationID)
			if err == nil {
				route = info.Route
			}
		}
		if route == "" {
			return nil
		}
		gateway, err := r.s.Gateways.Get(ctx, route)
		if err != nil {
			return nil
		}
		arg = strings.ToLower(arg)
		var rows []Info
		for _, model := range gateway.Models {
			id := model.ID()
			if arg != "" && !strings.Contains(strings.ToLower(id), arg) {
				continue
			}
			desc := "on " + gateway.ID
			if id == r.s.ModelID {
				desc = "* " + desc
			}
			rows = append(rows, Info{Usage: "/" + cmd + " " + id, Desc: desc})
		}
		return rows
	}
}

// completeMessages builds the /edit completer: editable messages in the
// active conversation — user messages only, newest first — so the id is
// picked instead of recalled. FillOnly because the replacement text still
// has to be typed after the id.
func (r *Registry) completeMessages(cmd string) func(context.Context, string) []Info {
	return func(ctx context.Context, arg string) []Info {
		if r.s.ConversationID == "" || strings.IndexFunc(arg, unicode.IsSpace) >= 0 {
			return nil
		}
		messages, err := r.s.Conversations.History(ctx, r.s.ConversationID, -50, 0)
		if err != nil {
			return nil
		}
		arg = strings.ToLower(arg)
		var rows []Info
		for i := len(messages) - 1; i >= 0; i-- {
			message := messages[i]
			if !message.Editable() {
				continue
			}
			if arg != "" &&
				!strings.Contains(strings.ToLower(message.ID()), arg) &&
				!strings.Contains(strings.ToLower(message.Content()), arg) {
				continue
			}
			preview := term.Sanitize(message.Content())
			if r := []rune(preview); len(r) > 40 {
				preview = string(r[:40]) + "…"
			}
			// Usage carries the trailing space — Enter fills the composer
			// at the text position, ready for the replacement.
			rows = append(rows, Info{Usage: "/" + cmd + " " + message.ID() + " ", Desc: preview, FillOnly: true})
		}
		return rows
	}
}

// completeAttachPaths completes /attach with workspace paths — spaces
// included, attachmentPath takes them verbatim. Bare /attach keeps its
// pending-attachments list, so the empty arg completes nothing;
// directories fill and keep listing, files submit the attach.
func (r *Registry) completeAttachPaths(_ context.Context, arg string) []Info {
	if arg == "" {
		return nil
	}
	dir, prefix := ".", arg
	if i := strings.LastIndex(arg, "/"); i >= 0 {
		dir, prefix = arg[:i], arg[i+1:]
	}
	if dir == "" {
		dir = "."
	}
	entries, err := fs.ReadDir(r.s.Workspace.FS(), dir)
	if err != nil {
		return nil
	}
	var dirs, files []Info
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) || (strings.HasPrefix(name, ".") && !strings.HasPrefix(prefix, ".")) {
			continue
		}
		path := name
		if dir != "." {
			path = dir + "/" + name
		}
		if entry.IsDir() {
			dirs = append(dirs, Info{Usage: "/attach " + path + "/", Desc: "dir", FillOnly: true})
			continue
		}
		desc := ""
		if info, err := entry.Info(); err == nil {
			desc = fmt.Sprintf("%d B", info.Size())
		}
		files = append(files, Info{Usage: "/attach " + path, Desc: desc})
	}
	return append(dirs, files...)
}

func (r *Registry) commandHelp(_ context.Context, emitter service.CommandEmitter, args string) error {
	if strings.TrimSpace(args) != "" {
		return errors.New("usage: /help")
	}
	r.s.Write(emitter, "Commands:\n")
	for _, info := range r.Describe() {
		r.s.Write(emitter, "  %-24s %s\n", info.Usage, info.Desc)
	}
	r.s.Write(emitter, "Ctrl+C cancels the active turn; Ctrl+D exits at the prompt; Tab completes commands and their arguments.\n")
	r.s.Write(emitter, "Agent tool calls run automatically; exec is unsandboxed. Review command activity.\n")
	return nil
}

func (r *Registry) commandExit(_ context.Context, emitter service.CommandEmitter, args string) error {
	if strings.TrimSpace(args) != "" {
		return errors.New("usage: /exit")
	}
	r.s.Exit = true
	r.s.Write(emitter, "goodbye\n")
	return nil
}

func (r *Registry) commandNew(ctx context.Context, emitter service.CommandEmitter, args string) error {
	if strings.TrimSpace(args) != "" {
		return errors.New("usage: /new")
	}
	if r.s.ConversationID != "" {
		info, err := r.s.Conversations.Info(ctx, r.s.ConversationID)
		if err != nil {
			return err
		}
		if r.s.Route == "" {
			r.s.Route = info.Route
		}
		if r.s.ModelID == "" {
			if r.s.Route == info.Route {
				r.s.ModelID = info.ModelID
			} else if gateway, err := r.s.Gateways.Get(ctx, r.s.Route); err == nil && !gateway.Broken && session.HasModel(gateway.Models, info.ModelID) {
				r.s.ModelID = info.ModelID
			}
		}
	}
	r.s.ConversationID = ""
	r.s.Write(emitter, "new conversation; route=%q model=%q\n", r.s.Route, r.s.ModelID)
	return nil
}

func (r *Registry) commandResume(ctx context.Context, emitter service.CommandEmitter, args string) error {
	id := strings.TrimSpace(args)
	if id == "" {
		if conversations, err := r.s.Conversations.List(ctx); err != nil {
			return err
		} else if len(conversations) == 0 {
			return errors.New("no saved conversations")
		}
		return errors.New("usage: /resume <conversation-id>")
	}
	if strings.IndexFunc(id, unicode.IsSpace) >= 0 {
		return errors.New("usage: /resume <conversation-id>")
	}
	conv, err := r.s.Conversations.Info(ctx, id)
	if err != nil {
		return err
	}
	messages, err := r.s.Conversations.History(ctx, id, -defaultHistoryLimit, 0)
	if err != nil {
		return err
	}
	r.s.ConversationID = id
	r.s.Route, r.s.ModelID = "", "" // use the binding stored with the conversation
	r.s.Write(emitter, "resumed %s (route=%s model=%s)\n", id, conv.Route, conv.ModelID)
	r.writeHistory(emitter, messages)
	return nil
}

func (r *Registry) commandList(ctx context.Context, emitter service.CommandEmitter, args string) error {
	if strings.TrimSpace(args) != "" {
		return errors.New("usage: /list")
	}
	conversations, err := r.s.Conversations.List(ctx)
	if err != nil {
		return err
	}
	if len(conversations) == 0 {
		r.s.Write(emitter, "no saved conversations\n")
		return nil
	}
	for _, conv := range conversations {
		marker := " "
		if conv.ID == r.s.ConversationID {
			marker = "*"
		}
		r.s.Write(emitter, "%s %s route=%s model=%s messages=%d", marker, conv.ID, conv.Route, conv.ModelID, conv.MessageCount)
		if conv.Title != "" {
			r.s.Write(emitter, " title=%q", conv.Title)
		}
		if !conv.UpdatedAt.IsZero() {
			r.s.Write(emitter, " updated=%s", conv.UpdatedAt.Local().Format(time.RFC3339))
		}
		r.s.Write(emitter, "\n")
	}
	return nil
}

const defaultHistoryLimit = 20

func (r *Registry) commandHistory(ctx context.Context, emitter service.CommandEmitter, args string) error {
	if r.s.ConversationID == "" {
		return errors.New("no active conversation; use /resume <id>")
	}
	limit := defaultHistoryLimit
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
	messages, err := r.s.Conversations.History(ctx, r.s.ConversationID, -limit, 0)
	if err != nil {
		return err
	}
	r.writeHistory(emitter, messages)
	return nil
}

func (r *Registry) writeHistory(emitter service.CommandEmitter, messages []entity.Message) {
	for _, message := range messages {
		// Persisted content replays raw bytes — provider-controlled
		// strings carry the same escape-injection risk as live deltas.
		r.s.Write(emitter, "%s %s", message.ID(), message.Role())
		if message.Content() != "" {
			r.s.Write(emitter, ": %s", term.Sanitize(message.Content()))
		}
		r.s.Write(emitter, "\n")
		for _, attachment := range message.Attachments() {
			r.s.Write(emitter, "  [attachment %s %s]\n", term.Sanitize(attachment.Name()), attachment.Modality())
		}
		for _, call := range message.ToolCalls() {
			r.s.Write(emitter, "  [tool %s %s] %s\n", term.Sanitize(call.Name()), term.Sanitize(call.ID()), term.Sanitize(call.Input()))
			if call.Output() != "" {
				r.s.Write(emitter, "  [tool result %s] %s\n", term.Sanitize(call.ID()), term.Sanitize(call.Output()))
			}
			for _, attachment := range call.OutputAttachments() {
				r.s.Write(emitter, "  [tool attachment %s %s]\n", term.Sanitize(attachment.Name()), attachment.Modality())
			}
		}
	}
}

func (r *Registry) commandRoute(ctx context.Context, emitter service.CommandEmitter, args string) error {
	id := strings.TrimSpace(args)
	if id == "" {
		gateways, err := r.s.Gateways.List(ctx)
		if err != nil {
			return err
		}
		if len(gateways) == 0 {
			r.s.Write(emitter, "no configured gateways — add one: core gateway add <id> --style <style> --endpoint <url> --models <m> --key-stdin\n")
			return nil
		}
		selected := r.s.Route
		if selected == "" && r.s.ConversationID != "" {
			info, err := r.s.Conversations.Info(ctx, r.s.ConversationID)
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
			r.s.Write(emitter, "%s %s %s style=%s models=%d %s%s\n", marker, gateway.ID, gateway.Name, gateway.Style, len(gateway.Models), key, broken)
		}
		return nil
	}
	if strings.IndexFunc(id, unicode.IsSpace) >= 0 {
		return errors.New("usage: /route [gateway-id]")
	}
	gateway, err := r.s.Gateways.Get(ctx, id)
	if err != nil {
		return err
	}
	if gateway.Broken {
		return fmt.Errorf("gateway %q is broken and cannot be selected", id)
	}
	r.s.Route = gateway.ID
	if r.s.ModelID != "" && !session.HasModel(gateway.Models, r.s.ModelID) {
		r.s.ModelID = ""
	}
	r.s.Write(emitter, "route set to %s (%s)\n", gateway.ID, gateway.Name)
	return nil
}

func (r *Registry) commandModel(ctx context.Context, emitter service.CommandEmitter, args string) error {
	route, selected := r.s.Route, r.s.ModelID
	if r.s.ConversationID != "" && (route == "" || selected == "") {
		info, err := r.s.Conversations.Info(ctx, r.s.ConversationID)
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
	gateway, err := r.s.Gateways.Get(ctx, route)
	if err != nil {
		return err
	}
	if gateway.Broken {
		return fmt.Errorf("gateway %q is broken and cannot be used", route)
	}
	id := strings.TrimSpace(args)
	if id == "" {
		if len(gateway.Models) == 0 {
			r.s.Write(emitter, "gateway %s has no models\n", route)
			return nil
		}
		for _, model := range gateway.Models {
			marker := " "
			if model.ID() == selected {
				marker = "*"
			}
			r.s.Write(emitter, "%s %s\n", marker, model.ID())
		}
		return nil
	}
	if strings.IndexFunc(id, unicode.IsSpace) >= 0 {
		return errors.New("usage: /model [model-id]")
	}
	if !session.HasModel(gateway.Models, id) {
		return fmt.Errorf("model %q is not in gateway %q; use /model to list", id, route)
	}
	r.s.ModelID = id
	r.s.Write(emitter, "model set to %s on %s\n", id, route)
	return nil
}

func (r *Registry) commandAttach(_ context.Context, emitter service.CommandEmitter, args string) error {
	if strings.TrimSpace(args) == "" {
		if len(r.s.Attachments) == 0 {
			r.s.Write(emitter, "no pending attachments; /attach <path> adds one\n")
			return nil
		}
		for _, a := range r.s.Attachments {
			r.s.Write(emitter, "  %s (%s)\n", a.Name(), a.Modality())
		}
		return nil
	}
	path, err := attachmentPath(args)
	if err != nil {
		return err
	}
	attachment, err := attach.Resolve(r.s.Workspace, r.s.WorkspacePath, path)
	if err != nil {
		return err
	}
	r.s.Attachments = append(r.s.Attachments, attachment)
	r.s.Write(emitter, "attached %s (%s) for the next turn\n", attachment.Name(), attachment.Modality())
	return nil
}

func (r *Registry) commandDetach(_ context.Context, emitter service.CommandEmitter, args string) error {
	if strings.TrimSpace(args) != "" {
		return errors.New("usage: /detach")
	}
	if len(r.s.Attachments) == 0 {
		r.s.Write(emitter, "no pending attachments\n")
		return nil
	}
	r.s.Write(emitter, "dropped %d attachment(s)\n", len(r.s.Attachments))
	r.s.Attachments = nil
	return nil
}

// commandCancel interrupts the in-flight send/edit/compact through the
// cancel func the model bound when the job started.
func (r *Registry) commandCancel(_ context.Context, emitter service.CommandEmitter, args string) error {
	if strings.TrimSpace(args) != "" {
		return errors.New("usage: /cancel")
	}
	if r.s.CancelTurn == nil {
		r.s.Write(emitter, "no active turn\n")
		return nil
	}
	r.s.CancelTurn()
	r.s.Write(emitter, "interrupt requested\n")
	return nil
}

// commandClear raises the one-shot flag the update loop turns into
// tea.ClearScreen — the registry cannot emit commands itself.
func (r *Registry) commandClear(_ context.Context, emitter service.CommandEmitter, args string) error {
	if strings.TrimSpace(args) != "" {
		return errors.New("usage: /clear")
	}
	if !r.s.Interactive {
		r.s.Write(emitter, "clear needs an interactive terminal\n")
		return nil
	}
	r.s.ClearQueued = true
	return nil
}

// commandCompact is the direct path — the model intercepts /compact so the
// fold runs as a background job; called from the registry it blocks.
func (r *Registry) commandCompact(ctx context.Context, emitter service.CommandEmitter, args string) error {
	if strings.TrimSpace(args) != "" {
		return errors.New("usage: /compact")
	}
	if r.s.ConversationID == "" {
		return errors.New("no active conversation; use /resume <id>")
	}
	if err := r.s.Conversations.Compact(ctx, r.s.ConversationID); err != nil {
		return err
	}
	r.s.Write(emitter, "compacted %s\n", r.s.ConversationID)
	return nil
}

func (r *Registry) commandCopy(ctx context.Context, emitter service.CommandEmitter, args string) error {
	if strings.TrimSpace(args) != "" {
		return errors.New("usage: /copy")
	}
	if r.s.ConversationID == "" {
		return errors.New("no active conversation; use /resume <id>")
	}
	messages, err := r.s.Conversations.History(ctx, r.s.ConversationID, -50, 0)
	if err != nil {
		return err
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if content := messages[i].Content(); messages[i].Role() == enum.RoleAssistant && content != "" {
			if err := clipboard.WriteAll(content); err != nil {
				return fmt.Errorf("clipboard unavailable: %w", err)
			}
			r.s.Write(emitter, "copied %d bytes from %s\n", len(content), messages[i].ID())
			return nil
		}
	}
	return errors.New("no assistant answer to copy")
}

func (r *Registry) commandDelete(ctx context.Context, emitter service.CommandEmitter, args string) error {
	id := strings.TrimSpace(args)
	if strings.IndexFunc(id, unicode.IsSpace) >= 0 {
		return errors.New("usage: /delete [conversation-id]")
	}
	if id == "" {
		id = r.s.ConversationID
	}
	if id == "" {
		return errors.New("no active conversation; /delete <id> or /resume first")
	}
	if err := r.s.Conversations.Delete(ctx, id); err != nil {
		return err
	}
	if id == r.s.ConversationID {
		r.s.ConversationID = "" // the binding died with the room
	}
	r.s.Write(emitter, "deleted %s\n", id)
	return nil
}

func (r *Registry) commandDiff(ctx context.Context, emitter service.CommandEmitter, args string) error {
	if strings.TrimSpace(args) != "" {
		return errors.New("usage: /diff")
	}
	const cap = 64 << 10
	// `-- .` confines the diff to the workspace subtree — bare `git -C dir
	// diff` reports the whole enclosing repository when the workspace is
	// not the repo root.
	out, err := exec.CommandContext(ctx, "git", "-C", r.s.WorkspacePath, "diff", "HEAD", "--", ".").CombinedOutput()
	if err != nil && strings.Contains(string(out), "invalid object name") {
		out, err = exec.CommandContext(ctx, "git", "-C", r.s.WorkspacePath, "diff", "--", ".").CombinedOutput()
	}
	if err != nil {
		return fmt.Errorf("git diff failed: %s", strings.TrimSpace(string(out)))
	}
	text := strings.TrimRight(string(out), "\n")
	if text == "" {
		r.s.Write(emitter, "no changes in the workspace\n")
		return nil
	}
	if len(text) > cap {
		text = text[:cap] + "\n… truncated, run git diff directly for the full patch"
	}
	r.s.Write(emitter, "%s\n", text)
	return nil
}

func (r *Registry) commandExport(ctx context.Context, emitter service.CommandEmitter, args string) error {
	name := strings.TrimSpace(args)
	if name == "" {
		return errors.New("usage: /export <file> — path stays inside the workspace")
	}
	if name[0] == '"' { // same quoting rule as /attach
		if unq, err := strconv.Unquote(name); err == nil {
			name = unq
		}
	}
	if r.s.ConversationID == "" {
		return errors.New("no active conversation; use /resume <id>")
	}
	messages, err := r.s.Conversations.History(ctx, r.s.ConversationID, 0, 0)
	if err != nil {
		return err
	}
	var out strings.Builder
	fmt.Fprintf(&out, "# conversation %s\n", r.s.ConversationID)
	for _, message := range messages {
		fmt.Fprintf(&out, "\n## %s · %s\n\n%s\n", message.Role(), message.ID(), message.Content())
		for _, a := range message.Attachments() {
			fmt.Fprintf(&out, "[attachment %s %s]\n", a.Name(), a.Modality())
		}
		for _, call := range message.ToolCalls() {
			fmt.Fprintf(&out, "[tool %s %s] %s\n", call.Name(), call.ID(), call.Input())
			if call.Output() != "" {
				fmt.Fprintf(&out, "[tool result %s] %s\n", call.ID(), call.Output())
			}
		}
	}
	f, err := r.s.Workspace.Create(name)
	if err != nil {
		return fmt.Errorf("export path must stay inside the workspace: %w", err)
	}
	if _, err := f.WriteString(out.String()); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	r.s.Write(emitter, "exported %d messages to %s\n", len(messages), name)
	return nil
}

func (r *Registry) commandPwd(_ context.Context, emitter service.CommandEmitter, args string) error {
	if strings.TrimSpace(args) != "" {
		return errors.New("usage: /pwd")
	}
	r.s.Write(emitter, "%s\n", r.s.WorkspacePath)
	return nil
}

func (r *Registry) commandStatus(ctx context.Context, emitter service.CommandEmitter, args string) error {
	if strings.TrimSpace(args) != "" {
		return errors.New("usage: /status")
	}
	route, modelID := r.s.Route, r.s.ModelID
	if r.s.ConversationID == "" {
		r.s.Write(emitter, "no active conversation\n")
	} else {
		info, err := r.s.Conversations.Info(ctx, r.s.ConversationID)
		if err != nil {
			return err
		}
		if route == "" {
			route = info.Route
		}
		if modelID == "" {
			modelID = info.ModelID
		}
		r.s.Write(emitter, "conversation %s\n", info.ID)
		if info.Title != "" {
			r.s.Write(emitter, "title       %q\n", info.Title)
		}
		r.s.Write(emitter, "messages    %d active", info.ActiveCount)
		if info.SealedSegments > 0 {
			r.s.Write(emitter, " (%d total, %d sealed segments)", info.MessageCount, info.SealedSegments)
		}
		r.s.Write(emitter, "\ncreated     %s\nupdated     %s\n",
			info.CreatedAt.Local().Format(time.RFC3339), info.UpdatedAt.Local().Format(time.RFC3339))
	}
	r.s.Write(emitter, "route       %s\nmodel       %s\n", route, modelID)
	if n := len(r.s.Attachments); n > 0 {
		r.s.Write(emitter, "attachments %d pending\n", n)
	}
	r.s.Write(emitter, "workspace   %s\n", r.s.WorkspacePath)
	return nil
}

func (r *Registry) commandTitle(ctx context.Context, emitter service.CommandEmitter, args string) error {
	title := strings.TrimSpace(args)
	if title == "" {
		return errors.New("usage: /title <text>")
	}
	if r.s.ConversationID == "" {
		return errors.New("no active conversation; use /resume <id>")
	}
	if err := r.s.Conversations.Rename(ctx, r.s.ConversationID, title); err != nil {
		return err
	}
	r.s.Write(emitter, "title set to %q\n", title)
	return nil
}

func (r *Registry) commandTools(_ context.Context, emitter service.CommandEmitter, args string) error {
	if strings.TrimSpace(args) != "" {
		return errors.New("usage: /tools")
	}
	schemas := r.s.Conversations.ToolSchemas()
	if len(schemas) == 0 {
		r.s.Write(emitter, "no tools registered\n")
		return nil
	}
	for _, s := range schemas {
		r.s.Write(emitter, "  %-14s %-12s %s\n", s.Name(), s.Permission(), s.Description())
	}
	return nil
}

func (r *Registry) commandUsage(ctx context.Context, emitter service.CommandEmitter, args string) error {
	if strings.TrimSpace(args) != "" {
		return errors.New("usage: /usage")
	}
	if r.s.ConversationID == "" {
		return errors.New("no active conversation; use /resume <id>")
	}
	info, err := r.s.Conversations.Info(ctx, r.s.ConversationID)
	if err != nil {
		return err
	}
	u := info.Usage
	r.s.Write(emitter, "input        %d\ncached read  %d\ncached write %d\noutput       %d\ntotal        %d\n",
		u.InputTokens(), u.CacheReadTokens(), u.CacheWriteTokens(), u.OutputTokens(), u.Total())
	return nil
}

// commandEdit is the registry path — the model intercepts /edit first so
// the regeneration streams through the turn pump; direct invocation edits
// silently.
func (r *Registry) commandEdit(ctx context.Context, emitter service.CommandEmitter, args string) error {
	if r.s.ConversationID == "" {
		return errors.New("no active conversation; use /resume <id>")
	}
	messageID, content, ok := SplitFirst(args)
	if !ok || strings.TrimSpace(content) == "" {
		return errors.New("usage: /edit <message-id> <replacement text>")
	}
	return r.s.Edit(ctx, r.s.ConversationID, messageID, content, nil)
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

// SplitFirst cuts the first whitespace-delimited token off raw input.
func SplitFirst(raw string) (first, rest string, ok bool) {
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
