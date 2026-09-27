package cmdattach

import (
	"context"
	"fmt"
	"io/fs"
	"strings"

	appcommand "github.com/jahrulnr/nusallm/application/command"
	"github.com/jahrulnr/nusallm/infrastructure/domain/service"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

// AttachCommand is the /attach command.
type AttachCommand struct{ h appcommand.SlashSession }

// New binds /attach to a session host.
func New(h appcommand.SlashSession) dservice.SlashCommand { return &AttachCommand{h: h} }

func (c *AttachCommand) Name() string        { return "attach" }
func (c *AttachCommand) Usage() string       { return "/attach [path]" }
func (c *AttachCommand) Description() string { return "attach a workspace file to the next turn" }

func (c *AttachCommand) Complete(ctx context.Context, arg string) []dservice.CommandCompletion {
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
	entries, err := fs.ReadDir(c.h.FS(), dir)
	if err != nil {
		return nil
	}
	var dirs, files []service.CommandCompletion
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
			dirs = append(dirs, service.CommandCompletion{Usage: "/attach " + path + "/", Desc: "dir", FillOnly: true})
			continue
		}
		desc := ""
		if info, err := entry.Info(); err == nil {
			desc = fmt.Sprintf("%d B", info.Size())
		}
		files = append(files, service.CommandCompletion{Usage: "/attach " + path, Desc: desc})
	}
	rows := append(dirs, files...)
	if len(rows) == 0 {
		return nil
	}
	return rows
}

func (c *AttachCommand) Execute(_ context.Context, _ string, emitter dservice.CommandEmitter, args ...string) error {
	raw := ""
	if len(args) > 0 {
		raw = args[0]
	}
	if strings.TrimSpace(raw) == "" {
		if len(c.h.Attachments()) == 0 {
			c.h.Write(emitter, "no pending attachments; /attach <path> adds one\n")
			return nil
		}
		for _, a := range c.h.Attachments() {
			c.h.Write(emitter, "  %s (%s)\n", a.Name(), a.Modality())
		}
		return nil
	}
	workspace, err := valueobject.ParseWorkspacePath(raw)
	if err != nil {
		return err
	}
	attachment, err := c.h.ResolveAttachment(workspace.Path())
	if err != nil {
		return err
	}
	c.h.AppendAttachment(attachment)
	c.h.Write(emitter, "attached %s (%s) for the next turn\n", attachment.Name(), attachment.Modality())
	return nil
}
