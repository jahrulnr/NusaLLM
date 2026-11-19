package cmddiff

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"unicode/utf8"

	appcommand "github.com/jahrulnr/nusallm/application/command"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
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
	var out []byte
	var err error
	// `git diff HEAD` only has meaning once a first commit exists — and the
	// failure message for an unborn branch differs across Git versions
	// ("invalid object name", "bad revision 'HEAD'", "ambiguous argument").
	// Probe the ref instead of parsing that error text.
	if probe := exec.CommandContext(ctx, "git", "-C", c.h.Path(), "rev-parse", "--verify", "--quiet", "HEAD"); probe.Run() == nil {
		out, err = exec.CommandContext(ctx, "git", "-C", c.h.Path(), "diff", "HEAD", "--", ".").CombinedOutput()
	} else {
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
		// A byte cap can land inside a multibyte rune and leave an
		// invalid UTF-8 tail no terminal can render. Back up to the
		// rune start — the same boundary rule the websocket delta
		// splitter applies to its chunk cuts.
		end := cap
		if !utf8.RuneStart(text[end]) {
			start := end
			for start > 0 && end-start < utf8.UTFMax && !utf8.RuneStart(text[start]) {
				start--
			}
			if utf8.RuneStart(text[start]) {
				if _, size := utf8.DecodeRuneInString(text[start:]); start+size > end {
					end = start
				}
			}
		}
		text = text[:end] + "\n… truncated, run git diff directly for the full patch"
	}
	c.h.Write(emitter, "%s\n", text)
	return nil
}
