package transport

// T-03a row 7 — a provider 5xx is a provider failure, never an engine fault,
// and 503 keeps the local/unavailable distinction: the code decides whether
// the client retries the same route or looks elsewhere.

import (
	"errors"
	"net/http"
	"testing"

	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
)

func TestClassifyErrorSplitsProviderFromEngineFaults(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
		code string
	}{
		// FromStatus marks the error upstream — what every adapter produces.
		{"provider 503", apperror.FromStatus(503, "overloaded", errors.New("body")), 503, "upstream_error"},
		{"provider 500", apperror.FromStatus(500, "boom", errors.New("body")), 500, "upstream_error"},
		{"provider 529", apperror.FromStatus(529, "overloaded", errors.New("body")), 529, "upstream_error"},
		{"provider 502", apperror.FromStatus(502, "bad gateway", errors.New("body")), 502, "upstream_error"},
		{"provider 504", apperror.FromStatus(504, "timeout", errors.New("body")), 504, "upstream_timeout"},
		{"provider 429", apperror.FromStatus(429, "rate limited", errors.New("body")), 429, "upstream_error"},
		// Local failures are the engine's own.
		{"local 503", apperror.New(http.StatusServiceUnavailable, "server shutting down", nil), 503, "unavailable"},
		{"local 500", apperror.New(http.StatusInternalServerError, "engine broke", nil), 500, "internal"},
		{"local 429", apperror.New(http.StatusTooManyRequests, "mailbox full", nil), 429, "queue_full"},
		{"local 408", apperror.New(http.StatusRequestTimeout, "canceled", nil), 408, "interrupted"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, code, _ := ClassifyError(tc.err)
			if status != tc.want || code != tc.code {
				t.Fatalf("ClassifyError = %d/%q, want %d/%q", status, code, tc.want, tc.code)
			}
		})
	}
}
