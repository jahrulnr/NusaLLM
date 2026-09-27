package cmddiff

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	appcommand "github.com/jahrulnr/llm-base/application/command"
	dservice "github.com/jahrulnr/llm-base/infrastructure/domain/service"
)

// DiffCommand is the /diff command.
type DiffCommand struct{ h appcommand.SlashSession }

// New binds /diff to a session host.
func New(h appcommand.SlashSession) dservice.SlashCommand { return &DiffCommand{h: h} }

func (c *DiffCommand) Name() string        { return "diff" }
func (c *DiffCommand) Usage() string       { return "/diff" }
func (c *DiffCommand) Description() string { return "show the workspace git diff" }

func (c *DiffCommand) Execute(ctx context.Context, _ string, emitter dservice.CommandEmitter, args ...string) error {
	raw := ""
	if len(args) > 0 {
		raw = args[0]
	}
	if strings.TrimSpace(raw) != "" {
		return errors.New("usage: /diff")
	}
	const cap = 64 << 10
	out, err := exec.CommandContext(ctx, "git", "-C", c.h.Path(), "diff", "HEAD", "--", ".").CombinedOutput()
	if err != nil && strings.Contains(string(out), "invalid object name") {
		out, err = exec.CommandContext(ctx, "git", "-C", c.h.Path(), "diff", "--", ".").CombinedOutput()
	}
	if err != nil {
		return fmt.Errorf("git diff failed: %s", strings.TrimSpace(string(out)))
	}
	text := strings.TrimRight(string(out), "\n")
	if text == "" {
		c.h.Write(emitter, "no changes in the workspace\n")
		return nil
	}
	if len(text) > cap {
		text = text[:cap] + "\n… truncated, run git diff directly for the full patch"
	}
	c.h.Write(emitter, "%s\n", text)
	return nil
}
