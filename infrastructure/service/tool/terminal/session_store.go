package toolexec

import (
	"cmp"
	"context"
	"math/rand/v2"
	"slices"
	"sync"

	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
)

const (
	// maxSessionsPerRoom caps the registry the way codex caps
	// MAX_UNIFIED_EXEC_PROCESSES — past it, the least-recently-touched
	// sessions are killed, protecting the newest protectedSessions.
	maxSessionsPerRoom = 64
	protectedSessions  = 8
	sessionIDMin       = 1000
	sessionIDSpan      = 100_000 - sessionIDMin
)

// SessionStore is the TerminalSessions registry: rooms map conversation id
// to its live sessions. Session goroutines are detached from any caller
// ctx — the store, not a job, owns their lifetime.
type SessionStore struct {
	mu    sync.Mutex
	rooms map[string]map[int]*terminalSession
}

func NewSessionStore() *SessionStore {
	return &SessionStore{rooms: map[string]map[int]*terminalSession{}}
}

// Open spawns and registers the session before returning — the entry must
// exist in the map before the caller's first Collect so a racing
// TerminateConversation already sees it.
func (s *SessionStore) Open(ctx context.Context, convID string, req dservice.TerminalOpenRequest) (dservice.TerminalSession, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	sess, err := spawnSession(convID, 0, req)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	room := s.rooms[convID]
	if room == nil {
		room = map[int]*terminalSession{}
		s.rooms[convID] = room
	}
	sess.id = s.allocID(room)
	room[sess.id] = sess
	evicted := s.pruneLocked(room)
	s.mu.Unlock()
	for _, dead := range evicted {
		dead.Terminate()
	}
	return sess, nil
}

func (s *SessionStore) Get(convID string, id int) (dservice.TerminalSession, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.rooms[convID][id]
	if !ok {
		return nil, false
	}
	sess.touch()
	return sess, true
}

// Terminate kills one session. The session is removed from the registry
// first so a concurrent Get can never return a dead session under a
// recycled id.
func (s *SessionStore) Terminate(convID string, id int) bool {
	s.mu.Lock()
	sess, ok := s.rooms[convID][id]
	if ok {
		delete(s.rooms[convID], id)
	}
	s.mu.Unlock()
	if !ok {
		return false
	}
	sess.Terminate()
	return true
}

func (s *SessionStore) TerminateConversation(convID string) {
	s.mu.Lock()
	room := s.rooms[convID]
	delete(s.rooms, convID)
	s.mu.Unlock()
	for _, sess := range room {
		sess.Terminate()
	}
}

func (s *SessionStore) TerminateAll() {
	s.mu.Lock()
	rooms := s.rooms
	s.rooms = map[string]map[int]*terminalSession{}
	s.mu.Unlock()
	for _, room := range rooms {
		for _, sess := range room {
			sess.Terminate()
		}
	}
}

// allocID picks an unused per-room id in [1000, 100000) — codex's range.
// Random (not sequential) so a recycled id cannot be guessed; the room map
// itself is the reserved set.
func (s *SessionStore) allocID(room map[int]*terminalSession) int {
	for {
		id := sessionIDMin + rand.IntN(sessionIDSpan)
		if _, taken := room[id]; !taken {
			return id
		}
	}
}

// pruneLocked enforces the room cap: the protectedSessions most recently
// touched entries are untouchable; among the rest, exited sessions go
// first, then oldest. Eviction returns sessions to kill — Terminate runs
// outside the store lock because it joins goroutines.
func (s *SessionStore) pruneLocked(room map[int]*terminalSession) []*terminalSession {
	if len(room) <= maxSessionsPerRoom {
		return nil
	}
	sessions := make([]*terminalSession, 0, len(room))
	for _, sess := range room {
		sessions = append(sessions, sess)
	}
	// Eviction order mirrors codex: already-exited entries go first,
	// then least-recently-touched — a live process outranks a zombie.
	slices.SortFunc(sessions, func(a, b *terminalSession) int {
		if a.exited.Load() != b.exited.Load() {
			if a.exited.Load() {
				return -1
			}
			return 1
		}
		return cmp.Compare(a.lastUsed.Load(), b.lastUsed.Load())
	})
	var evicted []*terminalSession
	for i, sess := range sessions {
		if len(room)-len(evicted) <= maxSessionsPerRoom {
			break
		}
		if len(sessions)-i <= protectedSessions {
			break
		}
		evicted = append(evicted, sess)
		delete(room, sess.id)
	}
	return evicted
}

// Compile-time check the registry satisfies the domain port.
var _ dservice.TerminalSessions = (*SessionStore)(nil)
