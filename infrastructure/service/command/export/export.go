package cmdexport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	appcommand "github.com/jahrulnr/nusallm/application/command"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

// ExportCommand is the /export command.
type ExportCommand struct{ h appcommand.SlashSession }

// New binds /export to a session host.
func New(h appcommand.SlashSession) dservice.SlashCommand { return &ExportCommand{h: h} }

func (c *ExportCommand) Name() string        { return "export" }
func (c *ExportCommand) Usage() string       { return "/export <file>" }
func (c *ExportCommand) Description() string { return "write the transcript to a workspace file" }

func (c *ExportCommand) Execute(ctx context.Context, _ string, emitter dservice.CommandEmitter, args ...string) error {
	raw := ""
	if len(args) > 0 {
		raw = args[0]
	}
	workspace, err := valueobject.ParseWorkspacePath(raw)
	if err != nil {
		return errors.New("usage: /export <file> — path stays inside the workspace")
	}
	name := workspace.Path()
	if c.h.ConversationID() == "" {
		return errors.New("no active conversation; use /resume <id>")
	}
	messages, err := c.h.ConversationHistory(ctx, c.h.ConversationID(), 0, 0)
	if err != nil {
		return err
	}
	var out strings.Builder
	fmt.Fprintf(&out, "# conversation %s\n", c.h.ConversationID())
	for _, message := range messages {
		// A pure injection message — no text, every call an application
		// context record — exports as a context block: one line per
		// stored output, no empty heading or tool dumps. Either carrier
		// (the old user-role pins, current system-role) takes it.
		injections := 0
		for _, call := range message.ToolCalls() {
			if call.Type() == enum.TooltypeInjection {
				injections++
			}
		}
		if message.Content() == "" &&
			injections > 0 && injections == len(message.ToolCalls()) {
			fmt.Fprintf(&out, "\n## context · %s\n\n", message.ID())
			for _, call := range message.ToolCalls() {
				if call.HasOutput() {
					fmt.Fprintf(&out, "%s\n", call.Output())
				}
			}
			continue
		}
		fmt.Fprintf(&out, "\n## %s · %s\n\n%s\n", message.Role(), message.ID(), message.Content())
		for _, a := range message.Attachments() {
			fmt.Fprintf(&out, "[attachment %s %s]\n", a.Name(), a.Modality())
		}
		for _, call := range message.ToolCalls() {
			if call.Type() == enum.TooltypeInjection {
				// Only a recorded output earns a context line — a bare
				// injection call must not export an empty line.
				if call.HasOutput() {
					fmt.Fprintf(&out, "%s\n", call.Output())
				}
				continue
			}
			fmt.Fprintf(&out, "[tool %s %s] %s\n", call.Name(), call.ID(), call.Input())
			if call.HasOutput() {
				fmt.Fprintf(&out, "[tool result %s] %s\n", call.ID(), call.Output())
			}
		}
	}
	f, err := c.h.CreateFile(name)
	if err != nil {
		return fmt.Errorf("export path must stay inside the workspace: %w", err)
	}
	if _, err := io.WriteString(f, out.String()); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	c.h.Write(emitter, "exported %d messages to %s\n", len(messages), name)
	return nil
}
