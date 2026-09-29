// Package apperror is the shared error contract: an HTTP status code plus
// a public Message safe for frontend display, wrapping an internal Cause
// that never leaves the backend. One taxonomy covers HTTP and file I/O —
// consumers route on StatusCode() via As(), not on error-string matching.
//
// Routing conventions for LLM calls: 401/403 = dead credentials, 429 =
// rate-limited (try another route), 413 = context overflow (compact, don't
// fallback), 502/503/504 = upstream unreachable (retry or fallback), other
// 4xx = invalid request (fix the payload, don't retry as-is).
package apperror

import (
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
)

// HTTP status codes used across the codebase.
const (
	StatusBadRequest          = 400
	StatusUnauthorized        = 401
	StatusForbidden           = 403
	StatusNotFound            = 404
	StatusRequestTimeout      = 408
	StatusPayloadTooLarge     = 413
	StatusUnprocessableEntity = 422
	StatusTooManyRequests     = 429

	StatusInternalServerError = 500
	StatusBadGateway          = 502
	StatusServiceUnavailable  = 503
	StatusGatewayTimeout      = 504
)

// IAppError is the error contract: status for routing, Message for the
// frontend, Cause for internal diagnostics.
type IAppError interface {
	StatusCode() int
	Error() string
	Unwrap() error
	CausedBy(err error) IAppError
	Caused() string
	ErrCause() error
	SetCaller(string)
	CalledAt() string
	IsUpstream() bool
}

// AppError carries a status code, a public message, and an internal cause.
// Upstream marks provider-originated failures so consumers can split a
// shared status — an upstream 429 is a provider rate limit, a local 429
// is a full queue.
type AppError struct {
	Code     int
	Message  string
	Cause    error
	Caller   string
	Upstream bool
}

// New builds an AppError — code must be a valid HTTP status (100-599).
func New(code int, message string, causedBy error) IAppError {
	return newAt(code, message, causedBy, 2)
}

// newAt captures Caller at the given stack depth so helpers that forward
// through it still blame their own caller — one stack walk per error.
func newAt(code int, message string, causedBy error, skip int) IAppError {
	if code < 100 || code > 599 {
		message = fmt.Sprintf("invalid status code provided: %d", code)
		causedBy = fmt.Errorf("invalid status code: %d", code)
		code = StatusInternalServerError
	}
	_, file, line, _ := runtime.Caller(skip)
	return &AppError{
		Code:    code,
		Message: message,
		Cause:   causedBy,
		Caller:  fmt.Sprintf("%s:%d", filepath.Base(file), line),
	}
}

func (e *AppError) Error() string   { return e.Message }
func (e *AppError) Unwrap() error   { return e.Cause }
func (e *AppError) StatusCode() int { return e.Code }

func (e *AppError) SetCaller(v string) { e.Caller = v }
func (e *AppError) CalledAt() string   { return e.Caller }

// IsUpstream reports whether the failure originated at the provider.
func (e *AppError) IsUpstream() bool { return e.Upstream }

// Caused renders code + public message + internal cause for logs.
func (e *AppError) Caused() string {
	cause := e.Cause
	if cause == nil {
		cause = errors.New("unknown")
	}
	return fmt.Sprintf("error %d: %s (caused by: %v)", e.Code, e.Message, cause)
}

// ErrCause exposes Caused() as an error value.
func (e *AppError) ErrCause() error { return errors.New(e.Caused()) }

// CausedBy returns a copy with a new cause, re-pointing Caller here.
func (e *AppError) CausedBy(err error) IAppError {
	n := newAt(e.Code, e.Message, err, 2).(*AppError)
	n.Upstream = e.Upstream
	return n
}

// FromStatus adapts an upstream HTTP status into an AppError — the shared
// baseline for adapters, marked IsUpstream so consumers can tell a
// provider failure from a local one on the same status. message must be
// safe for the frontend; raw upstream bodies belong in causedBy.
func FromStatus(status int, message string, causedBy error) IAppError {
	if status < 400 || status > 599 {
		status = StatusBadGateway
	}
	e := newAt(status, message, causedBy, 2).(*AppError)
	e.Upstream = true
	return e
}

func NewNotFoundError(resource string, causedBy error) IAppError {
	return newAt(StatusNotFound, resource+" not found", causedBy, 2)
}

func NewUnauthorizedError(message string, causedBy error) IAppError {
	return newAt(StatusUnauthorized, message, causedBy, 2)
}

func NewForbiddenError(message string, causedBy error) IAppError {
	return newAt(StatusForbidden, message, causedBy, 2)
}

func NewInternalError(message string, causedBy error) IAppError {
	return newAt(StatusInternalServerError, message, causedBy, 2)
}

// As unwraps err into an IAppError via errors.As. Non-AppError causes fall
// back to a 500 "internal error" — no information leaks to Message.
func As(err error) IAppError {
	if appErr, ok := errors.AsType[IAppError](err); ok {
		return appErr
	}
	return New(StatusInternalServerError, "internal error", err)
}
