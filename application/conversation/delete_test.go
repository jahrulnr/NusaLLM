package appconversation

import (
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
)

// Deleting an idle room removes storage and retires its worker.
func TestDeleteIdle(t *testing.T) {
	app, _ := fixture(t, okHandler)
	if _, err := app.Send(t.Context(), "room", "gw1", "m1", "hi", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := app.Delete(t.Context(), "room"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.deps.Convs.Load(t.Context(), "room"); apperror.As(err).StatusCode() != 404 {
		t.Fatalf("room must be gone: %v", err)
	}
	waitWorkers(t, app, 0)
}

// Delete on a nonexistent room is an honest 404 — and the worker spawned
// to answer it evaporates.
func TestDeleteAbsentRoom(t *testing.T) {
	app, _ := fixture(t, okHandler)
	if err := app.Delete(t.Context(), "ghost"); apperror.As(err).StatusCode() != 404 {
		t.Fatalf("delete missing room → %v, want 404", err)
	}
	waitWorkers(t, app, 0)
}

// Delete preempts a live turn: the running send dies with 408, storage
// goes, and no late persistence may resurrect the room.
func TestDeleteWhileLive(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	app, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		<-release
		okHandler(w, r)
	})
	sent := make(chan error, 1)
	go func() { _, err := app.Send(t.Context(), "room", "gw1", "m1", "ping", nil, nil); sent <- err }()
	time.Sleep(50 * time.Millisecond) // send is holding the slot

	if err := app.Delete(t.Context(), "room"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-sent:
		if apperror.As(err).StatusCode() != 408 {
			t.Fatalf("killed send → %v, want 408", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("delete did not reach the running send")
	}
	if _, err := app.deps.Convs.Load(t.Context(), "room"); apperror.As(err).StatusCode() != 404 {
		t.Fatalf("room must be gone: %v", err)
	}
	waitWorkers(t, app, 0)
}

// A queued send behind the killed one gets 410 — the room it was waiting
// for no longer exists.
func TestDeleteFlushesPending(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	var calls int32
	app, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		<-release
		okHandler(w, r)
	})
	go app.Send(t.Context(), "room", "gw1", "m1", "first", nil, nil)
	time.Sleep(50 * time.Millisecond)
	queued := make(chan error, 1)
	go func() { _, err := app.Send(t.Context(), "room", "", "", "second", nil, nil); queued <- err }()
	time.Sleep(50 * time.Millisecond) // second is queued

	if err := app.Delete(t.Context(), "room"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-queued:
		if apperror.As(err).StatusCode() != 410 {
			t.Fatalf("queued send after delete → %v, want 410", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("queued send not flushed by delete")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("queued send reached upstream after delete: %d calls", got)
	}
	waitWorkers(t, app, 0)
}

// The id is reusable after delete — a Send mints a fresh room with no
// trace of the old history.
func TestSendAfterDeleteMintsFreshRoom(t *testing.T) {
	app, _ := fixture(t, okHandler)
	if _, err := app.Send(t.Context(), "room", "gw1", "m1", "old life", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := app.Delete(t.Context(), "room"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Send(t.Context(), "room", "gw1", "m1", "new life", nil, nil); err != nil {
		t.Fatal(err)
	}
	conv, err := app.deps.Convs.Load(t.Context(), "room")
	if err != nil {
		t.Fatal(err)
	}
	msgs := conv.Messages()
	if len(msgs) != 2 || msgs[0].Content() != "new life" || msgs[0].Role() != enum.RoleUser {
		t.Fatalf("resurrected room carries old history: %+v", msgs)
	}
}
