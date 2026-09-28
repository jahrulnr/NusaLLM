package coretext

import (
	"net/http"
	"time"

	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
)

type Options struct {
	Client      *http.Client
	Logger      drepo.Logger
	Referer     string        // → HTTP-Referer
	Title       string        // → X-Title
	FileRoot    string        // root for local file:// attachments; empty disables local refs
	IdleTimeout time.Duration // per-chunk deadline; default 90s
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
