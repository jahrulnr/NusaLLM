package admin

import (
	"fmt"
	"io"
	"path/filepath"

	repoconversation "github.com/jahrulnr/nusallm/infrastructure/repository/conversation"
)

// ConversationRepair is an offline operator command for a Compact that
// stopped between publishing a sealed segment and its metadata counter.
func ConversationRepair(args []string, stdout, stderr io.Writer) int {
	if len(args) != 2 || (args[1] != "--discard-duplicate" && args[1] != "--commit") {
		fmt.Fprintln(stderr, "usage: nusallm conversation repair <id> --discard-duplicate|--commit (stop the app first)")
		return 2
	}
	cfg, _, err := LoadConfig(false)
	if err != nil {
		printErr(stderr, err)
		return 1
	}
	action := args[1][2:]
	if err := repoconversation.RepairOrphan(filepath.Join(cfg.Storage.Dir, "conversations"), args[0], action); err != nil {
		printErr(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "repaired conversation %s (%s)\n", args[0], action)
	return 0
}
