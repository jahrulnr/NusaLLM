// Package transport holds the delivery-wide contract mappings shared by
// the HTTP and WebSocket boundaries: the RFC 9457 problem shape, the
// stable error classification, and the Usage wire payload. Pure data and
// mapping only — no gin, no sockets — so either delivery can consume it
// without importing a sibling boundary.
package transport

import (
	"net/http"

	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
)

// Problem is the RFC 9457 error body — a stable machine code, the HTTP
// status, and display-safe detail. Cause never crosses the wire.
type Problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Code   string `json:"code"`
	Detail string `json:"detail,omitempty"`
}

// NewProblem builds the problem body for a status/code/detail triple.
func NewProblem(status int, code, detail string) Problem {
	return Problem{
		Type:   "urn:nusallm:problem:" + code,
		Title:  http.StatusText(status),
		Status: status,
		Code:   code,
		Detail: detail,
	}
}

// ClassifyError maps an application-layer error to the contract's
// status, stable machine code, and display-safe detail — shared by HTTP
// responses and WebSocket terminal frames. The public Message becomes
// detail; the internal Cause stays server-side. A 401/403 inside an
// AppError is a dead upstream credential — per contract it collapses to
// 502 upstream_error (the transport's own 403s are written directly as
// forbidden_origin).
func ClassifyError(err error) (status int, code, detail string) {
	ae := apperror.As(err)
	status = ae.StatusCode()
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		status = http.StatusBadGateway
	}
	return status, codeFor(status, ae.IsUpstream()), ae.Error()
}

// codeFor maps a status to the stable machine code — the closed set a UI
// routes on. upstream disambiguates 429: a provider rate limit reports
// upstream_error while a full conversation mailbox reports queue_full.
// Statuses outside the catalog fold to their nearest class.
func codeFor(status int, upstream bool) string {
	switch status {
	case http.StatusBadRequest:
		return "invalid_argument"
	case http.StatusForbidden:
		return "forbidden_origin"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusRequestTimeout:
		return "interrupted"
	case http.StatusGone:
		return "gone"
	case http.StatusRequestEntityTooLarge:
		return "input_too_large"
	case http.StatusTooManyRequests:
		if upstream {
			return "upstream_error"
		}
		return "queue_full"
	case http.StatusBadGateway:
		return "upstream_error"
	case http.StatusServiceUnavailable:
		return "unavailable"
	case http.StatusGatewayTimeout:
		return "upstream_timeout"
	}
	if status >= 500 {
		return "internal"
	}
	return "invalid_argument"
}
