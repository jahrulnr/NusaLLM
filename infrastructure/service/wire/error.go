package wire

import (
	"fmt"
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
// — is checked against the provider's own code and message text BEFORE
// FailureMessage bounds what reaches the frontend; body stays in Cause.
func StatusError(status int, publicMsg, code string, body []byte) error {
	if status == 400 && strings.Contains(code+" "+publicMsg, "context_length") {
		return apperror.FromStatus(413, "context length exceeded", UpstreamError(body))
	}
	return apperror.FromStatus(status, FailureMessage(status, publicMsg, code), UpstreamError(body))
}

// failureMessageMax bounds the toast-facing string — a provider rant
// stays whole in Cause and the diagnostics log instead.
const failureMessageMax = 200

// FailureMessage renders the public text for a rejected request: the
// provider's own message normalized to one bounded line, else a
// code/status fallback so a message-less body still says what happened.
func FailureMessage(status int, msg, code string) string {
	if s := strings.Join(strings.Fields(msg), " "); s != "" {
		if r := []rune(s); len(r) > failureMessageMax {
			s = strings.TrimRight(string(r[:failureMessageMax-1]), " ") + "…"
		}
		return s
	}
	if code != "" {
		return fmt.Sprintf("provider rejected the request (%s)", code)
	}
	return fmt.Sprintf("provider rejected the request (HTTP %d)", status)
}
