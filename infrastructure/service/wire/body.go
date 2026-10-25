package wire

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync/atomic"
	"time"

	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
)

// bodyCap bounds one upstream response body — the same 32 MiB every
// adapter applied through its local readAll.
const bodyCap = 32 << 20

// ReadBody reads a unary response body under the cap with an idle
// deadline: headers may arrive while the body stalls on a dead-but-open
// connection, and ResponseHeaderTimeout no longer applies — without the
// timer the read would park the turn until caller cancel. A body past the
// cap reports "too large" rather than surfacing a truncated payload as
// "malformed".
func ReadBody(ctx context.Context, body io.ReadCloser, idle time.Duration) ([]byte, error) {
	if idle <= 0 {
		return readCapped(body)
	}
	var fired atomic.Bool
	t := time.AfterFunc(idle, func() {
		fired.Store(true)
		_ = body.Close() // the read underneath errors out; Send's deferred close is idempotent
	})
	defer t.Stop()
	raw, err := readCapped(body)
	if err == nil {
		return raw, nil
	}
	switch {
	case fired.Load():
		return nil, apperror.New(502, "upstream body idle", nil)
	case ctx.Err() != nil: // caller gave up — not an upstream failure
		return nil, apperror.New(408, "request canceled or timed out", ctx.Err())
	case isAppError(err):
		// Already classified — the over-cap "upstream body too large"
		// must surface as itself, not drown under a generic transport
		// message.
		return nil, err
	default:
		return nil, apperror.New(502, "unreadable upstream body", err)
	}
}

func isAppError(err error) bool {
	_, ok := errors.AsType[apperror.IAppError](err)
	return ok
}

func readCapped(r io.Reader) ([]byte, error) {
	var b bytes.Buffer
	if _, err := b.ReadFrom(io.LimitReader(r, bodyCap+1)); err != nil {
		return nil, err
	}
	if b.Len() > bodyCap {
		return nil, apperror.New(502, "upstream body too large", nil)
	}
	return b.Bytes(), nil
}
