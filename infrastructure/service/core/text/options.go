package coretext

import (
	"errors"
	"net/http"
	"time"

	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
)

type Options struct {
	Client      *http.Client
	Logger      drepo.Logger
	Referer     string          // → HTTP-Referer
	Title       string          // → X-Title
	FileRoot    string          // root for local file:// attachments; empty disables local refs
	FileStore   drepo.Workspace // injected storage for file:// attachments
	IdleTimeout time.Duration   // per-chunk deadline; default 90s
	// HeaderTimeout bounds the wait for a response's first byte on the
	// built-in transport; ignored when Client is supplied. Default 30s.
	HeaderTimeout time.Duration
}

// FileRoot picks the attachment-resolution root for one send: the
// conversation's bound workspace wins, the adapter's configured fallback
// covers unbound rooms.
func FileRoot(conv *aggregate.Conversation, fallback string) string {
	if ws := conv.Workspace(); ws != "" {
		return ws
	}
	return fallback
}

// MaxTokensOr resolves a wire-required output cap. An explicit ask beats
// the adapter fallback and the declared output limit — write-path
// validation already compared it to the raw context window. It does not
// beat the window left after reserved input tokens: the prompt this
// request already carries shares that window, so an explicit cap that
// still overflows it is reduced. With no declared window the explicit
// value is sent unchanged. Otherwise the fallback fills the field, never
// above the output limit or that same remaining window. An undeclared
// ceiling (zero) is ignored. A window that cannot hold the prompt plus
// any reply is an error: sending a non-positive cap is the same
// impossible request.
func MaxTokensOr(m entity.Model, fallback, reserved int) (int, error) {
	if reserved < 0 {
		reserved = 0
	}
	capN := fallback
	if mt := m.MaxOutputTokens(); mt != nil {
		capN = *mt
	} else if lim := m.OutputLimit(); lim > 0 && lim < capN {
		capN = lim
	}
	if cw := m.ContextWindow(); cw > 0 {
		room := cw - reserved
		if room < capN {
			capN = room
		}
	}
	if capN < 1 {
		return 0, errors.New("context window cannot hold the prompt and a reply")
	}
	return capN, nil
}
