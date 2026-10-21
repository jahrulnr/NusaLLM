package appconversation

import (
	"context"
	"sync"
	"testing"
	"time"

	appjob "github.com/jahrulnr/nusallm/application/job"
	"github.com/jahrulnr/nusallm/infrastructure/config"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
)

type blockingShutdownJob struct {
	appjob.Base
	started  chan struct{}
	canceled chan struct{}
	release  <-chan struct{}
}

func (j *blockingShutdownJob) Execute(execCtx context.Context) error {
	ctx := j.Cancellable(execCtx, context.Background())
	close(j.started)
	<-ctx.Done()
	close(j.canceled)
	<-j.release
	err := apperror.New(408, "test job canceled", ctx.Err())
	j.Reply(err)
	return err
}

func TestShutdownCancelsWaitsAndRejectsPosts(t *testing.T) {
	app := New(nil, nil, nil, config.NewConfig(), nil, "", nil, nil)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer func() {
		unblock()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = app.Shutdown(ctx)
	}()
	running := &blockingShutdownJob{
		started:  make(chan struct{}),
		canceled: make(chan struct{}),
		release:  release,
	}
	running.Result = make(chan error, 1)
	queued := &blockingShutdownJob{
		started:  make(chan struct{}),
		canceled: make(chan struct{}),
		release:  release,
	}
	queued.Result = make(chan error, 1)

	app.post("room", running)
	select {
	case <-running.started:
	case <-time.After(2 * time.Second):
		t.Fatal("running job did not start")
	}
	app.post("room", queued)
	app.mu.Lock()
	w := app.workers["room"]
	app.mu.Unlock()

	firstCtx := &signaledContext{Context: t.Context(), observed: make(chan struct{})}
	firstDone := make(chan error, 1)
	go func() { firstDone <- app.Shutdown(firstCtx) }()
	<-firstCtx.observed // shutdown state is set before it starts waiting.
	select {
	case <-running.canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown did not cancel the running job")
	}
	select {
	case err := <-queued.Result:
		if apperror.As(err).StatusCode() != 503 {
			t.Fatalf("drained job error = %v, want 503", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown did not settle the queued job")
	}
	select {
	case <-queued.started:
		t.Fatal("drained queued job must not execute")
	default:
	}

	secondCtx := &signaledContext{Context: t.Context(), observed: make(chan struct{})}
	secondDone := make(chan error, 1)
	go func() { secondDone <- app.Shutdown(secondCtx) }()
	<-secondCtx.observed
	app.mu.Lock()
	completion := app.shutdownDone
	app.mu.Unlock()
	if completion == nil {
		t.Fatal("Shutdown completion channel was not initialized")
	}

	result, err := app.Send(t.Context(), "late-room", "gw1", "m1", "late", nil, appjob.SendOptions{}, nil)
	if apperror.As(err).StatusCode() != 503 {
		t.Fatalf("post after shutdown → %v, want 503", err)
	}
	if result.UserMessagePersisted {
		t.Fatal("rejected post must not report a persisted user message")
	}
	select {
	case <-w.Done():
		t.Fatal("Shutdown returned the worker before its running job was released")
	default:
	}
	select {
	case err := <-firstDone:
		t.Fatalf("first Shutdown returned before worker.Done: %v", err)
	default:
	}
	select {
	case err := <-secondDone:
		t.Fatalf("concurrent Shutdown returned before shared completion: %v", err)
	default:
	}

	unblock()
	for name, done := range map[string]<-chan error{"first": firstDone, "second": secondDone} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("%s Shutdown: %v", name, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s Shutdown did not finish after worker release", name)
		}
	}
	select {
	case <-completion:
	case <-time.After(2 * time.Second):
		t.Fatal("shared Shutdown completion did not close")
	}
	select {
	case <-w.Done():
	default:
		t.Fatal("Shutdown returned before worker.Done")
	}
}
