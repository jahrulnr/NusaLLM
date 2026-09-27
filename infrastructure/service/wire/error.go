package wire

import (
	"strings"

	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
)

// UpstreamError wraps a raw response body as the internal cause — never
// let it reach a frontend-facing Message.
func UpstreamError(body []byte) error {
	if len(body) == 0 {
		return nil
	}
	return upstreamError(body)
}

type upstreamError string

func (e upstreamError) Error() string { return string(e) }

// StatusError applies the shared routing convention: an upstream status
// becomes an apperror whose code the application routes on. The
// context_length remap — 400 from the provider but semantically "compact"
// — is checked against the provider's own code and message text.
// publicMsg must be frontend-safe; body stays in Cause.
func StatusError(status int, publicMsg, code string, body []byte) error {
	if status == 400 && strings.Contains(code+" "+publicMsg, "context_length") {
		return apperror.FromStatus(413, "context length exceeded", UpstreamError(body))
	}
	return apperror.FromStatus(status, publicMsg, UpstreamError(body))
}
