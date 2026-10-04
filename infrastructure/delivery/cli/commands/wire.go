package commands

import (
	"github.com/jahrulnr/nusallm/infrastructure/delivery/cli/session"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	svccommand "github.com/jahrulnr/nusallm/infrastructure/service/command"
	cmdattach "github.com/jahrulnr/nusallm/infrastructure/service/command/attach"
	cmdcancel "github.com/jahrulnr/nusallm/infrastructure/service/command/cancel"
	cmdclear "github.com/jahrulnr/nusallm/infrastructure/service/command/clear"
	cmdcompact "github.com/jahrulnr/nusallm/infrastructure/service/command/compact"
	cmdcopy "github.com/jahrulnr/nusallm/infrastructure/service/command/copy"
	cmddelete "github.com/jahrulnr/nusallm/infrastructure/service/command/delete"
	cmddetach "github.com/jahrulnr/nusallm/infrastructure/service/command/detach"
	cmddiff "github.com/jahrulnr/nusallm/infrastructure/service/command/diff"
	cmdedit "github.com/jahrulnr/nusallm/infrastructure/service/command/edit"
	cmdexit "github.com/jahrulnr/nusallm/infrastructure/service/command/exit"
	cmdexport "github.com/jahrulnr/nusallm/infrastructure/service/command/export"
	cmdhelp "github.com/jahrulnr/nusallm/infrastructure/service/command/help"
	cmdhistory "github.com/jahrulnr/nusallm/infrastructure/service/command/history"
	cmdlist "github.com/jahrulnr/nusallm/infrastructure/service/command/list"
	cmdmodel "github.com/jahrulnr/nusallm/infrastructure/service/command/model"
	cmdnew "github.com/jahrulnr/nusallm/infrastructure/service/command/new"
	cmdpwd "github.com/jahrulnr/nusallm/infrastructure/service/command/pwd"
	cmdquit "github.com/jahrulnr/nusallm/infrastructure/service/command/quit"
	cmdresume "github.com/jahrulnr/nusallm/infrastructure/service/command/resume"
	cmdroute "github.com/jahrulnr/nusallm/infrastructure/service/command/route"
	cmdstatus "github.com/jahrulnr/nusallm/infrastructure/service/command/status"
	cmdtitle "github.com/jahrulnr/nusallm/infrastructure/service/command/title"
	cmdtools "github.com/jahrulnr/nusallm/infrastructure/service/command/tools"
	cmdusage "github.com/jahrulnr/nusallm/infrastructure/service/command/usage"
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
		return append(svccommand.Describe(withHelp), SteerInfo)
	})
	return svccommand.NewRegistry(append(base, help, quit)...)
}
