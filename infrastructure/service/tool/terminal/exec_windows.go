package toolexec

import (
	"context"
	"os/exec"
)

// shellCommand runs command via cmd /C. CommandContext's default Cancel
// kills only the immediate process — Windows has no portable
// process-group kill (taskkill /T would take another subprocess), so
// grandchildren may outlive a timeout here.
func shellCommand(ctx context.Context, command string) *exec.Cmd {
	return exec.CommandContext(ctx, "cmd", "/C", command)
}
