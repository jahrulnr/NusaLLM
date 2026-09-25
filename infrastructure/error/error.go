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
}

// AppError carries a status code, a public message, and an internal cause.
type AppError struct {
	Code    int
	Message string
	Cause   error
	Caller  string
}

// New builds an AppError — code must be a valid HTTP status (100-599).
func New(code int, message string, causedBy error) IAppError {
	if code < 100 || code > 599 {
		message = fmt.Sprintf("invalid status code provided: %d", code)
		causedBy = fmt.Errorf("invalid status code: %d", code)
		code = StatusInternalServerError
	}
	_, file, line, _ := runtime.Caller(1)
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
	ne := New(e.Code, e.Message, err).(*AppError)
	_, file, line, _ := runtime.Caller(1)
	ne.SetCaller(fmt.Sprintf("%s:%d", filepath.Base(file), line))
	return ne
}

// FromStatus adapts an upstream HTTP status into an AppError — the shared
// baseline for adapters. message must be safe for the frontend; raw
// upstream bodies belong in causedBy.
func FromStatus(status int, message string, causedBy error) IAppError {
	if status < 400 || status > 599 {
		status = StatusBadGateway
	}
	return New(status, message, causedBy)
}

// withCaller re-points Caller to the helper's own callsite — New inside a
// helper would otherwise blame this file.
func withCaller(e IAppError) IAppError {
	_, file, line, _ := runtime.Caller(2)
	e.SetCaller(fmt.Sprintf("%s:%d", filepath.Base(file), line))
	return e
}

func NewNotFoundError(resource string, causedBy error) IAppError {
	return withCaller(New(StatusNotFound, resource+" not found", causedBy))
}

func NewUnauthorizedError(message string, causedBy error) IAppError {
	return withCaller(New(StatusUnauthorized, message, causedBy))
}

func NewForbiddenError(message string, causedBy error) IAppError {
	return withCaller(New(StatusForbidden, message, causedBy))
}

func NewInternalError(message string, causedBy error) IAppError {
	return withCaller(New(StatusInternalServerError, message, causedBy))
}

// As unwraps err into an IAppError via errors.As. Non-AppError causes fall
// back to a 500 "internal error" — no information leaks to Message.
func As(err error) IAppError {
	var appErr IAppError
	if errors.As(err, &appErr) {
		return appErr
	}
	return New(StatusInternalServerError, "internal error", err)
}
