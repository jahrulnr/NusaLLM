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
)

func main() {
	args := os.Args[1:]
	// People repeat the invocation target via habit, argv[0]'s name, or
	// `go run ./cmd/nusallm/main.go` leaking the file into argv. Strip a
	// leading token while it is the program name or a file that exists on
	// disk (the `go run` build target); a token that merely ends in .go is
	// an argument, not noise. "core" is still stripped for old scripts.
	for len(args) > 0 {
		tok := args[0]
		isFile := false
		if fi, err := os.Stat(tok); err == nil && !fi.IsDir() {
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
		case "serve":
			if err := serve(args[1:]); err != nil {
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
  ` + admin.CLIName + ` serve [--port N] local WebUI API — loopback only (default port 8080)
  ` + admin.CLIName + ` --help           this text

Environment:
  CONFIG_PATH   path to the JSON config (default: embedded defaults);
                a missing file is generated once at startup

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
	eng, err := compose(cfg, b.Secrets(), b.ConfigPath())
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
	wd, err := os.Getwd()
	if err != nil {
		return err
	}

	repl, err := tui.NewREPL(tui.Options{
		Conversations: conversationApp,
		Gateways:      b.Gateways(),
		Workspace:     wd,
		HistoryDir:    filepath.Join(dir, "cli"),
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
