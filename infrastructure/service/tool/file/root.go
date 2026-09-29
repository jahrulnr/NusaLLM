package toolfile

import (
	"context"
	"errors"

	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	"github.com/jahrulnr/nusallm/infrastructure/domain/service"
)

// checkDir validates the tool's configured sandbox directory at
// construction — the root itself opens per call.
func checkDir(store drepo.Workspace, root string) (string, error) {
	if store == nil {
		return "", errors.New("workspace repository is required")
	}
	return store.CheckRoot(root)
}

// callRoot opens a repository workspace session for one call: the turn-scoped
// workspace dir (the ctx value the application sets) wins over the
// tool's configured directory, so a conversation can retarget its file
// tree. The caller owns Close.
func callRoot(ctx context.Context, store drepo.Workspace, fallback string) (drepo.WorkspaceSession, error) {
	dir := service.WorkspaceDir(ctx)
	if dir == "" {
		dir = fallback
	}
	return store.Open(ctx, dir)
}
