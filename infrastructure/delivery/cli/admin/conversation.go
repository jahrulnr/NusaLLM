package admin

import (
	"context"
	"fmt"
	"io"

	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
)

// ConversationRepair is an offline operator command for a Compact that
// stopped between publishing a sealed segment and its metadata counter.
// The handler parses argv; the repair itself is the store's recovery port
// — offline by definition, so no broker/worker sequences it.
func ConversationRepair(args []string, stdout, stderr io.Writer) int {
	var action drepo.RepairAction
	switch {
	case len(args) == 2 && args[1] == "--discard-duplicate":
		action = drepo.RepairDiscardDuplicate
	case len(args) == 2 && args[1] == "--commit":
		action = drepo.RepairCommit
	default:
		fmt.Fprintln(stderr, "usage: nusallm conversation repair <id> --discard-duplicate|--commit (stop the app first)")
		return 2
	}
	cfg, _, err := LoadConfig(false)
	if err != nil {
		printErr(stderr, err)
		return 1
	}
	if err := conversationStore(cfg).RepairOrphan(context.Background(), args[0], action); err != nil {
		printErr(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "repaired conversation %s (%s)\n", args[0], action)
	return 0
}
