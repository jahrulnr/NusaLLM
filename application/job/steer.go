package job

import (
	"sync"
	"sync/atomic"
)

// SteeringState is the outcome slot one submitted input occupies.
// Pending input still waits in the mailbox and can be recalled; claimed
// input was drained by the turn loop but its user line has not committed
// yet; persisted input is in history; recalled input was withdrawn
// before claim.
type SteeringState string

const (
	SteeringPending   SteeringState = "pending"
	SteeringClaimed   SteeringState = "claimed"
	SteeringPersisted SteeringState = "persisted"
	SteeringRecalled  SteeringState = "recalled"
)

// SteeringReceipt tracks one submitted input through claim and
// persistence. Transitions are driven by the owning mailbox under its
// lock, or by the turn loop after claim — never the reverse direction —
// so State reads stay lock-free.
type SteeringReceipt struct {
	state atomic.Value // SteeringState
}

func newSteeringReceipt() *SteeringReceipt {
	r := &SteeringReceipt{}
	r.state.Store(SteeringPending)
	return r
}

// State reports the input's last recorded outcome. A receipt the turn
// loop claimed but never committed stays claimed — the caller reads
// that as "unconsumed" rather than guessing.
func (r *SteeringReceipt) State() SteeringState {
	return r.state.Load().(SteeringState)
}

// markPersisted flips a claimed receipt once its user line committed —
// AppendMessage's bool decides, even when it also returned an error.
func (r *SteeringReceipt) markPersisted() {
	r.state.Store(SteeringPersisted)
}

// markRecalled flips a pending receipt withdrawn before claim.
func (r *SteeringReceipt) markRecalled() {
	r.state.Store(SteeringRecalled)
}

// markClaimed flips a pending receipt drained by the turn loop.
func (r *SteeringReceipt) markClaimed() {
	r.state.Store(SteeringClaimed)
}

// steeringEntry is one buffered input: text plus the optional receipt
// tracking its outcome (receipt-free on the CLI's push path).
type steeringEntry struct {
	text    string
	receipt *SteeringReceipt
}

// SteeringInput is the exact-turn mid-turn input mailbox shared by the
// CLI's out-of-band steer path and transports that bind one mailbox per
// admitted turn (SendOptions.Steering). Entries carry text plus an
// optional receipt; the turn loop claims pending entries atomically at
// inference safe points — after a tool result persisted, before the
// next provider call — then marks each persisted only once its user
// line commits. The lock never covers I/O or callbacks: claim is a
// state flip under the lock, persistence writes and event emission
// happen after it releases.
type SteeringInput struct {
	mu      sync.Mutex
	pending []steeringEntry
}

// NewSteeringInput creates an empty mailbox.
func NewSteeringInput() *SteeringInput { return &SteeringInput{} }

// Submit buffers text and returns the receipt that tracks its outcome.
func (s *SteeringInput) Submit(text string) *SteeringReceipt {
	r := newSteeringReceipt()
	s.mu.Lock()
	s.pending = append(s.pending, steeringEntry{text: text, receipt: r})
	s.mu.Unlock()
	return r
}

// push buffers receipt-free input — the worker's out-of-band Steer path.
func (s *SteeringInput) push(text string) {
	s.mu.Lock()
	s.pending = append(s.pending, steeringEntry{text: text})
	s.mu.Unlock()
}

// Recall withdraws one exact pending input — atomic against claim, so a
// receipt already drained or persisted can never be taken back.
// ok=false — unknown, claimed, persisted, or already recalled.
func (s *SteeringInput) Recall(r *SteeringReceipt) bool {
	if r == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.State() != SteeringPending {
		return false
	}
	for i, e := range s.pending {
		if e.receipt == r {
			s.pending = append(s.pending[:i], s.pending[i+1:]...)
			e.receipt.markRecalled()
			return true
		}
	}
	return false
}

// popLast withdraws the newest pending input — a recall returns the text
// so the caller can restore it instead of losing the line. An entry
// carrying a receipt is marked recalled, so the exact receipt the web
// submitted reflects the CLI withdrawal too.
func (s *SteeringInput) popLast() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pending) == 0 {
		return "", false
	}
	last := s.pending[len(s.pending)-1]
	s.pending = s.pending[:len(s.pending)-1]
	if last.receipt != nil {
		last.receipt.markRecalled()
	}
	return last.text, true
}

// claim detaches every pending entry oldest-first, flipping each receipt
// to claimed in the same critical section — a recall that loses the
// race finds a non-pending state, never a second delivery.
func (s *SteeringInput) claim() []steeringEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pending) == 0 {
		return nil
	}
	out := s.pending
	s.pending = nil
	for _, e := range out {
		if e.receipt != nil {
			e.receipt.markClaimed()
		}
	}
	return out
}
