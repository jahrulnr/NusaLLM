package wire

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

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
	if status == 400 && ContextOverflow(code, publicMsg) {
		return apperror.FromStatus(413, "context length exceeded", UpstreamError(body))
	}
	return apperror.FromStatus(status, FailureMessage(status, publicMsg, code), UpstreamError(body))
}

// RetryAfter parses a Retry-After header value — integer seconds or an
// HTTP-date — into a delay. Absent or unreadable means the provider
// named no wait.
func RetryAfter(header string) time.Duration {
	s := strings.TrimSpace(header)
	if s == "" {
		return 0
	}
	if secs, err := strconv.Atoi(s); err == nil {
		return max(time.Duration(secs)*time.Second, 0)
	}
	if t, err := http.ParseTime(s); err == nil {
		return max(time.Until(t), 0)
	}
	return 0
}

// WithRetryAfter stamps a provider-asked wait onto a routing error so the
// turn's retry gate can honor it. A non-AppError passes through untouched.
func WithRetryAfter(err error, d time.Duration) error {
	if d > 0 {
		if ae, ok := err.(*apperror.AppError); ok {
			ae.RetryDelay = d
		}
	}
	return err
}

// ContextOverflow spots context-window rejections across provider
// phrasings — a stable code, or the message fragments OpenAI-compatible
// servers use when they send no code at all ("This model's maximum
// context length is N tokens", llama.cpp/vLLM/LM Studio et al.). One
// shared definition so every adapter gets the 413 compact-remap.
func ContextOverflow(code, msg string) bool {
	s := strings.ToLower(code + " " + msg)
	for _, frag := range []string{
		"context_length", "context length", "context window",
		"maximum context", "maximum number of tokens",
		"prompt is too long", "input is too long",
	} {
		if strings.Contains(s, frag) {
			return true
		}
	}
	return false
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
