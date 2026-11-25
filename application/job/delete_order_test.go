package job

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
)

type failingDeleteConversations struct {
	drepo.Conversation
	err error
}

func (f failingDeleteConversations) Delete(context.Context, string) error { return f.err }

type recordingTerminals struct {
	dservice.TerminalSessions
	terminated atomic.Bool
}

func (t *recordingTerminals) TerminateConversation(string) { t.terminated.Store(true) }

// A failed repository delete leaves the room on disk. Its terminal
// sessions must survive too: reaping them first turns a retryable storage
// error into unrelated work loss and leaves the user with a room whose
// live processes disappeared.
func TestDeleteKeepsTerminalsWhenRepositoryDeleteFails(t *testing.T) {
	terms := &recordingTerminals{}
	j := &Delete{Deps: Deps{
		Convs:     failingDeleteConversations{err: errors.New("disk full")},
		Terminals: terms,
	}}
	j.Result = make(chan error, 1)
	j.Bind("room")
	if err := j.Execute(context.Background()); err == nil {
		t.Fatal("repository delete failure was swallowed")
	}
	if terms.terminated.Load() {
		t.Fatal("terminal sessions were reaped despite the room still existing")
	}
}
