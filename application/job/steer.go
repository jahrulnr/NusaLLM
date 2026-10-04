package job

import "sync"

// steerInbox buffers mid-turn user input for one job. The turn loop
// drains it at inference safe points — after tool results persist,
// before the next provider call — so injected input can never bisect a
// tool-call/result pair or interleave with a streaming reply.
type steerInbox struct {
	mu    sync.Mutex
	items []string
}

func (s *steerInbox) push(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = append(s.items, text)
}

// popLast withdraws the newest pending input — a recall returns the text
// so the caller can restore it instead of losing the line.
func (s *steerInbox) popLast() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.items) == 0 {
		return "", false
	}
	last := s.items[len(s.items)-1]
	s.items = s.items[:len(s.items)-1]
	return last, true
}

// drain returns every pending input oldest-first and clears the inbox.
func (s *steerInbox) drain() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.items) == 0 {
		return nil
	}
	out := s.items
	s.items = nil
	return out
}
