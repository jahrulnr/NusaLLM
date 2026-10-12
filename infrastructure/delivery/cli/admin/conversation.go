package admin

import (
	"context"
	"fmt"
	"io"

	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
)

// ConversationRepair is an offline operator command for the crash windows
// segment surgery can leave: a Compact stopped between publishing a sealed
// segment and its metadata counter (--discard-duplicate or --commit), and
// a sealed-history edit stopped mid-commit (--finish-rewrite rolls the
// staged payload forward). The handler parses argv; the repair itself is
// the store's recovery port — offline by definition, so no broker/worker
// sequences it.
func ConversationRepair(args []string, stdout, stderr io.Writer) int {
	var action drepo.RepairAction
	var finishRewrite bool
	switch {
	case len(args) == 2 && args[1] == "--discard-duplicate":
		action = drepo.RepairDiscardDuplicate
	case len(args) == 2 && args[1] == "--commit":
		action = drepo.RepairCommit
	case len(args) == 2 && args[1] == "--finish-rewrite":
		finishRewrite = true
	default:
		fmt.Fprintln(stderr, "usage: nusallm conversation repair <id> --discard-duplicate|--commit|--finish-rewrite (stop the app first)")
		return 2
	}
	cfg, _, err := LoadConfig(false)
	if err != nil {
		printErr(stderr, err)
		return 1
	}
	store := conversationStore(cfg)
	verb := action.String()
	if finishRewrite {
		verb = "finish-rewrite"
		err = store.RepairRewrite(context.Background(), args[0])
	} else {
		err = store.RepairOrphan(context.Background(), args[0], action)
	}
	if err != nil {
		printErr(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "repaired conversation %s (%s)\n", args[0], verb)
	return 0
}
