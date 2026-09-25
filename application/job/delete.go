package job

import (
	"context"

	"github.com/jahrulnr/llm-base/infrastructure/worker"
)

// Delete removes the room — meta, live tail, every sealed segment — and
// retires the worker: returning worker.ErrTerminate after a successful
// delete makes the worker drain its queue (410 for everything queued)
// and exit. It preempts: a running job is interrupted first.
type Delete struct {
	Base
	Deps Deps
}

// IsPreempt marks delete as a control job — it cuts the queue and
// interrupts whatever is running.
func (j *Delete) IsPreempt() bool { return true }

// Execute deletes on a background ctx — an abandoned caller must not
// half-delete a room. finish translates ErrTerminate into a nil reply:
// the caller gets the op result, not the sentinel.
func (j *Delete) Execute(context.Context) (err error) {
	defer j.finish(&err)
	if err = j.Deps.Convs.Delete(context.Background(), j.ConvID()); err != nil {
		return err
	}
	return worker.ErrTerminate
}
