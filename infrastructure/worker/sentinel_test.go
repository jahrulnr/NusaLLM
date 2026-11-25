package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
)

// The port owns the sentinels. Every alias above must be the same value, or
// a caller on one side of the seam would stop recognizing an outcome
// produced on the other: Submit's refusal, Interrupt's idle answer, and the
// retire signal all cross it.
func TestSentinelAliasesAreThePortValues(t *testing.T) {
	cases := map[string][2]error{
		"closed":      {ErrClosed, dservice.ErrClosed},
		"full":        {ErrFull, dservice.ErrFull},
		"not running": {ErrNotRunning, dservice.ErrNotRunning},
		"terminate":   {ErrTerminate, dservice.ErrTerminate},
	}
	for label, pair := range cases {
		here, port := pair[0], pair[1]
		if here != port || !errors.Is(here, port) || !errors.Is(port, here) {
			t.Fatalf("%s: %v != %v — one sentinel must serve both packages", label, here, port)
		}
	}
}

// portJob only knows the port: it returns service.ErrTerminate, never the
// alias in this package. A job written against the contract alone must still
// retire a worker built here — that is what makes the seam real.
type portJob struct{}

func (portJob) Execute(context.Context) error { return dservice.ErrTerminate }
func (portJob) Interrupt()                    {}

func TestJobRetiresViaPortSentinel(t *testing.T) {
	terminated := make(chan []dservice.Job, 1)
	w := New(Options{OnTerminate: func(_ dservice.Retirable, drained []dservice.Job) {
		terminated <- drained
	}})
	if err := w.Submit(portJob{}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-terminated:
	case <-time.After(2 * time.Second):
		t.Fatal("a job returning service.ErrTerminate did not retire the worker")
	}
	waitClosed(t, w.Done(), "worker exit")
	if err := w.Submit(portJob{}); !errors.Is(err, dservice.ErrClosed) {
		t.Fatalf("submit after retirement → %v, want the port's ErrClosed", err)
	}
}
