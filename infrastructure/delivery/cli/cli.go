// Package cli exposes the conversation application as an interactive terminal REPL.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"

	"github.com/chzyer/readline"
	appconversation "github.com/jahrulnr/llm-base/application/conversation"
	appgateway "github.com/jahrulnr/llm-base/application/gateway"
	"github.com/jahrulnr/llm-base/infrastructure/domain/service"
	"github.com/jahrulnr/llm-base/infrastructure/domain/valueobject"
	apperror "github.com/jahrulnr/llm-base/infrastructure/error"
)

// Options wires the CLI to the conversation/gateway applications and its terminal workspace.
type Options struct {
	Conversations *appconversation.ConversationApp
	Gateways      *appgateway.GatewayApp
	Workspace     string
	HistoryDir    string
	Stdin         io.ReadCloser
	Stdout        io.Writer
	Stderr        io.Writer
}

// REPL owns the active conversation, route/model selection, and pending
// user-supplied attachments for one terminal session.
type REPL struct {
	conversations *appconversation.ConversationApp
	gateways      *appgateway.GatewayApp
	workspace     *os.Root
	workspacePath string
	historyFile   string
	stdin         io.ReadCloser
	stdout        io.Writer
	stderr        io.Writer
	commands      *commandRegistry

	conversationID string
	route          string
	modelID        string
	attachments    []valueobject.Attachment
	exit           bool
}

// NewREPL binds a REPL to the use cases, conversation store, and workspace
// root. Relative attachment paths are confined to that workspace.
func NewREPL(opts Options) (*REPL, error) {
	if opts.Conversations == nil || opts.Gateways == nil {
		return nil, errors.New("conversation and gateway apps are required")
	}
	if opts.HistoryDir == "" {
		return nil, errors.New("history directory is required")
	}
	if opts.Workspace == "" {
		opts.Workspace = "."
	}
	workspacePath, err := filepath.Abs(opts.Workspace)
	if err != nil {
		return nil, err
	}
	workspace, err := os.OpenRoot(workspacePath)
	if err != nil {
		return nil, err
	}
	historyFile, err := privateHistoryFile(opts.HistoryDir)
	if err != nil {
		_ = workspace.Close()
		return nil, err
	}
	if opts.Stdin == nil {
		opts.Stdin = os.Stdin
	}
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}
	r := &REPL{
		conversations: opts.Conversations,
		gateways:      opts.Gateways,
		workspace:     workspace,
		workspacePath: workspacePath,
		historyFile:   historyFile,
		stdin:         opts.Stdin,
		stdout:        opts.Stdout,
		stderr:        opts.Stderr,
	}
	r.commands = r.commandRegistry()
	return r, nil
}

// Close releases the workspace root held for confined attachment reads.
func (r *REPL) Close() error {
	return r.workspace.Close()
}

// Run reads commands and chat turns until EOF, /exit, or parent cancellation.
// Ctrl+C during a turn cancels that turn without terminating the REPL.
func (r *REPL) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	reader, err := readline.NewEx(&readline.Config{
		Prompt:            r.prompt(),
		HistoryFile:       r.historyFile,
		HistorySearchFold: true,
		AutoComplete:      commandCompleter{commands: r.commands},
		InterruptPrompt:   "^C",
		EOFPrompt:         "exit",
		Stdin:             r.stdin,
		Stdout:            r.stdout,
		Stderr:            r.stderr,
	})
	if err != nil {
		return err
	}
	readDone := make(chan struct{})
	var closeOnce sync.Once
	closeReader := func() { closeOnce.Do(func() { _ = reader.Close() }) }
	defer close(readDone)
	defer closeReader()
	go func() {
		select {
		case <-ctx.Done():
			closeReader()
		case <-readDone:
		}
	}()

	for !r.exit {
		if err := ctx.Err(); err != nil {
			return nil
		}
		line, err := r.readInput(reader)
		if ctx.Err() != nil {
			return nil
		}
		switch {
		case errors.Is(err, readline.ErrInterrupt):
			if _, writeErr := writeTerminal(r.stdout, "\n[input canceled]\n"); writeErr != nil {
				return writeErr
			}
			continue
		case errors.Is(err, io.EOF):
			return nil
		case err != nil:
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		input := parseInput(line)
		if input.command == "" && input.text == "" {
			continue
		}
		if input.command != "" {
			if err := r.executeCommand(ctx, input.command, input.args); err != nil {
				if writeErr := r.report(err); writeErr != nil {
					return writeErr
				}
			}
			continue
		}
		if err := r.send(ctx, input.text); err != nil {
			if writeErr := r.report(err); writeErr != nil {
				return writeErr
			}
		}
	}
	return nil
}

func (r *REPL) readInput(reader *readline.Instance) (string, error) {
	var lines []string
	for {
		prompt := r.prompt()
		if len(lines) > 0 {
			prompt = "... "
		}
		reader.SetPrompt(prompt)
		line, err := reader.Readline()
		if chmodErr := os.Chmod(r.historyFile, 0o600); chmodErr != nil {
			return "", chmodErr
		}
		if err != nil {
			return "", err
		}
		trailingSlashes := 0
		for i := len(line) - 1; i >= 0 && line[i] == '\\'; i-- {
			trailingSlashes++
		}
		continued := trailingSlashes%2 == 1
		if continued {
			line = line[:len(line)-1]
		}
		lines = append(lines, line)
		if !continued {
			return strings.Join(lines, "\n"), nil
		}
	}
}

func (r *REPL) prompt() string {
	if r.conversationID == "" {
		return "llm> "
	}
	return "[" + r.conversationID + "]> "
}

func (r *REPL) executeCommand(ctx context.Context, command, args string) error {
	var emitErr error
	emitter := service.CommandEmitter(func(chunk string) {
		if emitErr == nil {
			_, emitErr = writeTerminal(r.stdout, chunk)
		}
	})
	if err := r.commands.Execute(ctx, command, emitter, args); err != nil {
		return err
	}
	return emitErr
}

func (r *REPL) send(ctx context.Context, text string) error {
	if text == "" && len(r.attachments) == 0 {
		return nil
	}

	route, modelID := r.route, r.modelID
	if r.conversationID != "" {
		info, err := r.conversations.Info(ctx, r.conversationID)
		if err != nil {
			return err
		}
		if route == "" {
			route = info.Route
		}
		if modelID == "" {
			modelID = info.ModelID
		}
	}
	if route == "" || modelID == "" {
		return errors.New("select a route and model with /route and /model before starting a conversation")
	}
	gateway, err := r.gateways.Get(ctx, route)
	if err != nil {
		return err
	}
	if gateway.Broken {
		return fmt.Errorf("gateway %q is broken and cannot be used", route)
	}
	if !hasModel(gateway.Models, modelID) {
		return fmt.Errorf("model %q is not in gateway %q; use /model to select one", modelID, route)
	}
	for _, model := range gateway.Models {
		if model.ID() != modelID {
			continue
		}
		for _, attachment := range r.attachments {
			if !model.AcceptsInput(attachment.Modality()) {
				warning := fmt.Sprintf("warning: %s attachment %s will be omitted; model %s does not accept it\n",
					attachment.Modality(), attachment.Name(), modelID)
				if _, err := writeTerminal(r.stderr, warning); err != nil {
					return err
				}
			}
		}
		break
	}

	turnCtx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()
	renderer := newTurnRenderer(r.stdout)
	result, sendErr := r.conversations.Send(turnCtx, r.conversationID, route, modelID, text, r.attachments, renderer.Handle)
	if result.UserMessagePersisted {
		r.conversationID = result.ConversationID
		r.attachments = nil
	}
	if err := renderer.Err(); err != nil {
		return err
	}
	return sendErr
}

func (r *REPL) report(err error) error {
	message := err.Error()
	if appErr, ok := errors.AsType[apperror.IAppError](err); ok {
		message = appErr.Error()
	}
	_, writeErr := writeTerminal(r.stderr, "error: "+message+"\n")
	return writeErr
}

func (r *REPL) write(emitter service.CommandEmitter, format string, args ...any) {
	if emitter != nil {
		emitter(fmt.Sprintf(format, args...))
	}
}
