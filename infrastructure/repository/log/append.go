package repolog

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"
)

// record is the on-disk envelope: timestamp + grep tag + payload.
type record struct {
	Ts   time.Time `json:"ts"`
	Kind string    `json:"kind"`
	Data any       `json:"data"`
}

// Append writes one JSONL record and rotates once the active file reaches
// MaxLines. The record boundary is the crash window — a torn write drops
// the tail line on read, sealed segments stay intact. Entries round-trip
// through JSON before scrubbing, so RawMessage/struct payloads can't
// bypass the redactor.
func (l *Logger) Append(ctx context.Context, kind string, entry any) error {
	if strings.TrimSpace(kind) == "" {
		return errors.New("log kind is required")
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	raw, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	var normalized any
	if err := json.Unmarshal(raw, &normalized); err != nil {
		return err // not JSON-shaped → drop rather than log unscrubbed
	}
	raw, err = json.Marshal(record{Ts: time.Now(), Kind: kind, Data: l.redact(kind, normalized)})
	if err != nil {
		return err
	}
	if err := l.ensureFile(); err != nil {
		return err
	}
	if _, err := l.f.Write(append(raw, '\n')); err != nil {
		return err
	}
	if err := l.f.Sync(); err != nil {
		return err
	}
	l.lines++
	if l.maxLines > 0 && l.lines >= l.maxLines {
		return l.rotate()
	}
	return nil
}

// ensureFile attaches the active file when a previous write or rotation
// failure detached it — the logger never goes permanently dead: an Append
// retries the open instead of reporting a stale closed handle until
// restart. An explicit Close is honored and stays closed.
func (l *Logger) ensureFile() error {
	if l.closed {
		return os.ErrClosed
	}
	if l.f != nil {
		return nil
	}
	return l.reopen()
}
