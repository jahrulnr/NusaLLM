package commands

import (
	cmdattach "github.com/jahrulnr/nusallm/infrastructure/delivery/cli/commands/attach"
	cmdcancel "github.com/jahrulnr/nusallm/infrastructure/delivery/cli/commands/cancel"
	cmdclear "github.com/jahrulnr/nusallm/infrastructure/delivery/cli/commands/clear"
	cmdcompact "github.com/jahrulnr/nusallm/infrastructure/delivery/cli/commands/compact"
	cmdcopy "github.com/jahrulnr/nusallm/infrastructure/delivery/cli/commands/copy"
	cmddelete "github.com/jahrulnr/nusallm/infrastructure/delivery/cli/commands/delete"
	cmddetach "github.com/jahrulnr/nusallm/infrastructure/delivery/cli/commands/detach"
	cmddiff "github.com/jahrulnr/nusallm/infrastructure/delivery/cli/commands/diff"
	cmdedit "github.com/jahrulnr/nusallm/infrastructure/delivery/cli/commands/edit"
	cmdexit "github.com/jahrulnr/nusallm/infrastructure/delivery/cli/commands/exit"
	cmdexport "github.com/jahrulnr/nusallm/infrastructure/delivery/cli/commands/export"
	cmdhelp "github.com/jahrulnr/nusallm/infrastructure/delivery/cli/commands/help"
	cmdhistory "github.com/jahrulnr/nusallm/infrastructure/delivery/cli/commands/history"
	cmdlist "github.com/jahrulnr/nusallm/infrastructure/delivery/cli/commands/list"
	cmdmodel "github.com/jahrulnr/nusallm/infrastructure/delivery/cli/commands/model"
	cmdnew "github.com/jahrulnr/nusallm/infrastructure/delivery/cli/commands/new"
	cmdpwd "github.com/jahrulnr/nusallm/infrastructure/delivery/cli/commands/pwd"
	cmdquit "github.com/jahrulnr/nusallm/infrastructure/delivery/cli/commands/quit"
	cmdresume "github.com/jahrulnr/nusallm/infrastructure/delivery/cli/commands/resume"
	cmdroute "github.com/jahrulnr/nusallm/infrastructure/delivery/cli/commands/route"
	cmdstatus "github.com/jahrulnr/nusallm/infrastructure/delivery/cli/commands/status"
	cmdtitle "github.com/jahrulnr/nusallm/infrastructure/delivery/cli/commands/title"
	cmdtools "github.com/jahrulnr/nusallm/infrastructure/delivery/cli/commands/tools"
	cmdusage "github.com/jahrulnr/nusallm/infrastructure/delivery/cli/commands/usage"
	"github.com/jahrulnr/nusallm/infrastructure/delivery/cli/session"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
)

// New wires slash commands for one terminal session — each command is
// constructed explicitly, like tools in cmd/nusallm/main.go.
func New(s *session.Session) *Registry {
	h := Session(s)
	base := []dservice.SlashCommand{
		cmdattach.New(h),
		cmdcancel.New(h),
		cmdclear.New(h),
		cmdcompact.New(h),
		cmdcopy.New(h),
		cmddelete.New(h),
		cmddetach.New(h),
		cmddiff.New(h),
		cmdedit.New(h),
		cmdexit.New(h),
		cmdexport.New(h),
		cmdhistory.New(h),
		cmdlist.New(h),
		cmdmodel.New(h),
		cmdnew.New(h),
		cmdpwd.New(h),
		cmdresume.New(h),
		cmdroute.New(h),
		cmdstatus.New(h),
		cmdtitle.New(h),
		cmdtools.New(h),
		cmdusage.New(h),
	}
	quit := cmdquit.New(h)
	help := cmdhelp.New(h, func() []dservice.CommandCompletion {
		withHelp := append(append([]dservice.SlashCommand{}, base...), quit)
		return append(Describe(withHelp), SteerInfo)
	})
	return NewRegistry(append(base, help, quit)...)
}
