package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/jahrulnr/nusallm/infrastructure/delivery/cli/admin"
	reporuntime "github.com/jahrulnr/nusallm/infrastructure/repository/runtime"
	serviceruntime "github.com/jahrulnr/nusallm/infrastructure/service/runtime"
)

func runPython(args []string) int {
	root := os.Getenv("NUSALLM_RUNTIME_DIR")
	if root == "" {
		cfg, _, err := admin.LoadConfig(true)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		root = filepath.Join(cfg.Storage.Dir, "runtimes")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return admin.Python(ctx, args, os.Stdin, os.Stdout, os.Stderr, serviceruntime.NewPython(reporuntime.NewLocal(root), root))
}
