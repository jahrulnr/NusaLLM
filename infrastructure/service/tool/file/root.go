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

// callDir answers the workspace directory a call resolves paths in: the
// turn-scoped workspace dir (the ctx value the application sets) wins over
// the tool's configured directory, so a conversation can retarget its file
// tree. What the path argument means is the repository's decision
// (Workspace.LocateRead/LocateWrite), not this adapter's.
func callDir(ctx context.Context, fallback string) string {
	dir := service.WorkspaceDir(ctx)
	if dir == "" {
		dir = fallback
	}
	return dir
}
