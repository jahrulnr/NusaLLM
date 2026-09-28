package toolfile

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jahrulnr/nusallm/infrastructure/domain/service"
)

// checkDir validates the tool's configured sandbox directory at
// construction — the root itself opens per call.
func checkDir(root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	r, err := os.OpenRoot(abs)
	if err != nil {
		return "", err
	}
	return abs, r.Close()
}

// callRoot opens the os.Root sandbox for one call: the turn-scoped
// workspace dir (the ctx value the application sets) wins over the
// tool's configured directory, so a conversation can retarget its file
// tree. The caller owns Close.
func callRoot(ctx context.Context, fallback string) (root *os.Root, dir string, err error) {
	dir = service.WorkspaceDir(ctx)
	if dir == "" {
		dir = fallback
	}
	root, err = os.OpenRoot(dir)
	if err != nil {
		return nil, "", fmt.Errorf("workspace %q: %w", dir, err)
	}
	return root, dir, nil
}
