package coretext

import (
	"net/http"
	"time"

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
