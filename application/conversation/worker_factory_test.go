package appconversation

import (
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	"github.com/jahrulnr/nusallm/infrastructure/worker"
)

// testWorkers is the composition root for broker tests: the real executor,
// wired the same way cmd wires it, so the tests exercise the same port the
// daemon does.
func testWorkers(opts dservice.WorkerOptions) Worker { return worker.New(opts) }
