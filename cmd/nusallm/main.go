// Command nusallm loads configuration, wires repositories, service adapters,
// tools, and applications, then serves an interactive terminal REPL.
// Admin subcommands are routes on the CLI delivery boundary — this file
// is the mux, not the handler.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/jahrulnr/nusallm/infrastructure/delivery/cli/admin"
	"github.com/jahrulnr/nusallm/infrastructure/delivery/cli/tui"
	repofiletree "github.com/jahrulnr/nusallm/infrastructure/repository/filetree"
	repohistory "github.com/jahrulnr/nusallm/infrastructure/repository/history"
	repoworkspace "github.com/jahrulnr/nusallm/infrastructure/repository/workspace"
	"github.com/jahrulnr/nusallm/infrastructure/system"
)

func main() {
	fileTree := repofiletree.NewLocal()
	args := os.Args[1:]
	// People repeat the invocation target via habit — the binary name, or
	// a `go run` source path typed at the built binary — and `go run
	// ./cmd/nusallm` itself substitutes a temp binary for argv[0], so the
	// invoked name is matched rather than assumed. Strip a leading token
	// while it is the program name or a file that exists on disk; a token
	// that merely ends in .go is an argument, not noise. "core" is still
	// stripped for old scripts.
	for len(args) > 0 {
		tok := args[0]
		isFile := false
		if fi, err := fileTree.Stat(tok); err == nil && !fi.IsDir() {
			isFile = true
		}
		if tok != admin.CLIName && tok != "core" && tok != filepath.Base(os.Args[0]) && !isFile {
			break
		}
		args = args[1:]
	}
	if len(args) > 0 {
		switch args[0] {
		case "-h", "--help", "help":
			fmt.Print(usage)
			return
		case "onboard":
			os.Exit(admin.Onboard(os.Stdin, os.Stdout, os.Stderr))
		case "gateway":
			os.Exit(admin.Gateway(args[1:], os.Stdin, os.Stdout, os.Stderr))
		case "config":
			os.Exit(admin.Config(args[1:], os.Stdout, os.Stderr))
		case "conversation":
			if len(args) > 1 && args[1] == "repair" {
				os.Exit(admin.ConversationRepair(args[2:], os.Stdout, os.Stderr))
			}
			fmt.Fprintln(os.Stderr, "usage: nusallm conversation repair <id> --discard-duplicate|--commit|--finish-rewrite")
			os.Exit(2)
		case "serve":
			if len(args) > 1 && admin.IsServeAdmin(args[1]) {
				os.Exit(admin.ServeAdmin(args[1:], os.Stdout, os.Stderr, system.New()))
			}
			if err := serve(args[1:], system.New()); err != nil {
				fmt.Fprintln(os.Stderr, admin.CLIName+":", err)
				os.Exit(1)
			}
			return
		default:
			fmt.Fprintf(os.Stderr, "%s: unknown argument %q — try --help\n", admin.CLIName, args[0])
			os.Exit(2)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, admin.CLIName+":", err)
		os.Exit(1)
	}
}

var usage = admin.ProductName + ` — interactive LLM conversation REPL

Usage:
  ` + admin.CLIName + `                  interactive REPL
  ` + admin.CLIName + ` onboard          guided provider setup wizard
  ` + admin.CLIName + ` gateway <...>    manage provider routes — list/show/add/set/delete
  ` + admin.CLIName + ` config <...>     inspect and patch the config file — show/path/set/unset/validate
  ` + admin.CLIName + ` conversation repair <id> --discard-duplicate|--commit|--finish-rewrite (offline recovery)
  ` + admin.CLIName + ` serve [--port N] local WebUI — loopback only (default port 10998 from server.port)
  ` + admin.CLIName + ` serve install|start|stop|status|uninstall
                               install the WebUI for the current user; manage the host service
  ` + admin.CLIName + ` --help           this text

Environment:
  CONFIG_PATH   JSON config path. The installed binary defaults to
                ~/.config/nusallm/config.json. A source build still takes
                CONFIG_PATH from a .env file in the working directory.
                An exported CONFIG_PATH always wins. A missing file is
                generated once at startup

Provider setup (once per provider):
  ` + admin.CLIName + ` gateway add openai --style completion \
      --endpoint https://api.openai.com/v1 --models gpt-5 --key-stdin

Inside the REPL:
  /route, /model   pick a gateway and model before chatting
  /attach, /edit, /compact, /history, /status, /usage, /tools
  /help            full command list
  Ctrl+C           interrupt the active turn · Ctrl+D quits
`

func run(ctx context.Context) (runErr error) {
	b, err := admin.LoadBoot(true)
	if err != nil {
		return err
	}
	// The REPL owns config creation — a missing file is generated once
	// here so first-run UX has a file to point at.
	if err := admin.EnsureConfig(b.ConfigPath(), os.Stderr); err != nil {
		return err
	}
	defer func() {
		if closeErr := b.Close(); runErr == nil {
			runErr = closeErr
		}
	}()
	cfg := b.Config()
	dir := cfg.Storage.Dir
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	eng, err := compose(cfg, b.Secrets(), b.ConfigPath(), wd)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := eng.logger.Close(); runErr == nil {
			runErr = closeErr
		}
	}()
	conversationApp := eng.conversations
	defer func() {
		// Registered before NewREPL so its failure still retires workers.
		if shutdownErr := conversationApp.Shutdown(context.Background()); runErr == nil {
			runErr = shutdownErr
		}
	}()
	homeDir, _ := repofiletree.NewLocal().HomeDir()

	repl, err := tui.NewREPL(tui.Options{
		Conversations:  conversationApp,
		Gateways:       b.Gateways(),
		Workspace:      wd,
		WorkspaceStore: repoworkspace.NewLocal(),
		HistoryDir:     filepath.Join(dir, "cli"),
		HistoryStore:   repohistory.NewLocal(),
		HomeDir:        homeDir,
	})
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := repl.Close(); runErr == nil {
			runErr = closeErr
		}
	}()
	return repl.Run(ctx)
}
