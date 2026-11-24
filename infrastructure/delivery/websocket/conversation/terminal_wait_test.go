package wsconversation

// D-06 clause 4 — turn.failed is self-describing: when the provider asked
// for quiet, the terminal carries that wait so a client can hold Retry.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	appconversation "github.com/jahrulnr/nusallm/application/conversation"
	"github.com/jahrulnr/nusallm/infrastructure/domain/event"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
)

// quietErr is a provider failure carrying a Retry-After wait — the shape the
// adapters produce with wire.WithRetryAfter.
type quietErr struct {
	err   error
	delay time.Duration
}

func (e quietErr) Error() string             { return e.err.Error() }
func (e quietErr) Unwrap() error             { return e.err }
func (e quietErr) RetryAfter() time.Duration { return e.delay }

func TestTerminalCarriesProviderWait(t *testing.T) {
	h := newHub(t, stubSender{fn: func(_ context.Context, convID, _, _, _ string,
		_ event.TurnEventHandler) (appconversation.SendResult, error) {
		return appconversation.SendResult{ConversationID: convID, UserMessagePersisted: true},
			quietErr{err: apperror.FromStatus(429, "rate limited", errors.New("too many requests")), delay: 120 * time.Second}
	}})
	ws := dial(t, h)
	sendCommand(t, ws, `{"type":"turn.send","requestId":"r","payload":{"content":"x"}}`)
	f := readUntil(t, ws, "turn.failed")
	var p terminalPayload
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.Status != 429 || p.RetryAfterMs != 120_000 {
		t.Fatalf("terminal = %+v, want 429 with retryAfterMs 120000", p)
	}
}

func TestTerminalOmitsWaitWhenProviderNamedNone(t *testing.T) {
	h := newHub(t, stubSender{fn: func(_ context.Context, convID, _, _, _ string,
		_ event.TurnEventHandler) (appconversation.SendResult, error) {
		return appconversation.SendResult{ConversationID: convID, UserMessagePersisted: true},
			apperror.New(502, "upstream unreachable", errors.New("dial failed"))
	}})
	ws := dial(t, h)
	sendCommand(t, ws, `{"type":"turn.send","requestId":"r","payload":{"content":"x"}}`)
	f := readUntil(t, ws, "turn.failed")
	var raw map[string]any
	if err := json.Unmarshal(f.Payload, &raw); err != nil {
		t.Fatal(err)
	}
	if _, present := raw["retryAfterMs"]; present {
		t.Fatalf("terminal carries retryAfterMs without a provider wait: %s", f.Payload)
	}
}
