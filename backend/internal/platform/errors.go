// Package platform holds cross-cutting primitives shared by every domain:
// transport-level error envelopes, JSON response helpers and pagination.
package platform

import (
	"errors"
	"fmt"
	"net/http"
)

// ErrorCode is a stable, machine-readable identifier for a failure class.
// Clients are expected to branch on these rather than on HTTP status alone.
type ErrorCode string

const (
	CodeBadRequest    ErrorCode = "BAD_REQUEST"
	CodeUnauthorized  ErrorCode = "UNAUTHORIZED"
	CodeForbidden     ErrorCode = "FORBIDDEN"
	CodeNotFound      ErrorCode = "NOT_FOUND"
	CodeMethod        ErrorCode = "METHOD_NOT_ALLOWED"
	CodeConflict      ErrorCode = "CONFLICT"
	CodeValidation    ErrorCode = "VALIDATION_FAILED"
	CodeRateLimited   ErrorCode = "RATE_LIMITED"
	CodeInternal      ErrorCode = "INTERNAL_ERROR"
	CodeUnavailable   ErrorCode = "SERVICE_UNAVAILABLE"
	CodeUnimplemented ErrorCode = "NOT_IMPLEMENTED"
)

// FieldError describes a single invalid field on a request payload.
type FieldError struct {
	Field   string `json:"field"`
	Rule    string `json:"rule"`
	Message string `json:"message"`
}

// Error is the canonical application error. It carries the HTTP status it
// should map to, a stable code, optional field-level detail and an optional
// wrapped cause that is never leaked to the client.
type Error struct {
	Status  int
	Code    ErrorCode
	Message string
	Fields  []FieldError
	cause   error
}

func (e *Error) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.cause }

// WithCause attaches an internal error for logging and errors.Is/As traversal.
func (e *Error) WithCause(err error) *Error {
	e.cause = err
	return e
}

// WithFields attaches field-level validation detail.
func (e *Error) WithFields(fields ...FieldError) *Error {
	e.Fields = append(e.Fields, fields...)
	return e
}

func newError(status int, code ErrorCode, format string, args ...any) *Error {
	return &Error{Status: status, Code: code, Message: fmt.Sprintf(format, args...)}
}

// NewBadRequest reports a malformed or semantically invalid request.
func NewBadRequest(format string, args ...any) *Error {
	return newError(http.StatusBadRequest, CodeBadRequest, format, args...)
}

// NewUnauthorized reports a missing or invalid credential.
func NewUnauthorized(format string, args ...any) *Error {
	return newError(http.StatusUnauthorized, CodeUnauthorized, format, args...)
}

// NewForbidden reports an authenticated caller lacking the required grants.
func NewForbidden(format string, args ...any) *Error {
	return newError(http.StatusForbidden, CodeForbidden, format, args...)
}

// NewNotFound reports a missing resource. `resource` and `id` are safe to
// surface to the client.
func NewNotFound(resource string, id any) *Error {
	return newError(http.StatusNotFound, CodeNotFound, "%s %v was not found", resource, id)
}

// NewMethodNotAllowed reports that a route exists but does not accept the
// request's method. It is distinct from NewNotFound so a client can tell a
// wrong verb from a wrong path.
func NewMethodNotAllowed(method, path string) *Error {
	return newError(http.StatusMethodNotAllowed, CodeMethod,
		"%s is not supported on %s", method, path)
}

// NewConflict reports a uniqueness or state conflict, e.g. a duplicate key.
func NewConflict(format string, args ...any) *Error {
	return newError(http.StatusConflict, CodeConflict, format, args...)
}

// NewValidation aggregates one or more field-level validation failures.
func NewValidation(format string, args ...any) *Error {
	return newError(http.StatusUnprocessableEntity, CodeValidation, format, args...)
}

// NewRateLimited reports throttling.
func NewRateLimited(format string, args ...any) *Error {
	return newError(http.StatusTooManyRequests, CodeRateLimited, format, args...)
}

// NewInternal reports an unexpected server-side failure. The cause is logged
// but never returned to the client.
func NewInternal(format string, args ...any) *Error {
	return newError(http.StatusInternalServerError, CodeInternal, format, args...)
}

// NewUnavailable reports a dependency being unreachable or degraded.
func NewUnavailable(format string, args ...any) *Error {
	return newError(http.StatusServiceUnavailable, CodeUnavailable, format, args...)
}

// NewUnimplemented reports a route or capability that is not built yet.
func NewUnimplemented(format string, args ...any) *Error {
	return newError(http.StatusNotImplemented, CodeUnimplemented, format, args...)
}

// AsError coerces any error into an *Error, defaulting to a 500 envelope.
func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	var appErr *Error
	if errors.As(err, &appErr) {
		return appErr
	}
	return NewInternal("an unexpected error occurred").WithCause(err)
}
