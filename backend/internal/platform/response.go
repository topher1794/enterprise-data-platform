package platform

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
)

// errorBody is the wire representation of a failure.
type errorBody struct {
	Error struct {
		Code    ErrorCode    `json:"code"`
		Message string       `json:"message"`
		Fields  []FieldError `json:"fields,omitempty"`
	} `json:"error"`
}

// RequestIDHeader is the canonical header for correlating a request across
// service boundaries.
const RequestIDHeader = "X-Request-ID"

// JSON writes v as a JSON response with the given status code. It is the only
// place response bodies are produced for successful calls.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("failed to encode response body", "error", err)
	}
}

// NoContent writes an empty 204 response.
func NoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

// Fail writes err as a JSON error envelope. Non-application errors are
// downgraded to a generic 500 so internals never leak; the cause is logged.
func Fail(w http.ResponseWriter, r *http.Request, err error) {
	appErr := AsError(err)

	if appErr.Status >= http.StatusInternalServerError {
		slog.ErrorContext(r.Context(), "request failed",
			"error", appErr.Error(),
			"request_id", RequestIDFrom(r.Context()),
			"method", r.Method,
			"path", r.URL.Path,
		)
	}

	var body errorBody
	body.Error.Code = appErr.Code
	body.Error.Message = appErr.Message
	body.Error.Fields = appErr.Fields

	JSON(w, appErr.Status, body)
}

// FromRequest binds and decodes a JSON request body into dst, returning a
// client error on malformed payloads.
func FromRequest[T any](r *http.Request) (T, error) {
	var dst T

	if ct := r.Header.Get("Content-Type"); ct != "" && !isJSONContentType(ct) {
		var zero T
		return zero, NewBadRequest("unsupported Content-Type %q, expected application/json", ct)
	}

	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, MaxRequestBodyBytes))
	dec.DisallowUnknownFields()

	if err := dec.Decode(&dst); err != nil {
		var zero T
		return zero, NewBadRequest("request body could not be decoded: %s", err.Error())
	}
	return dst, nil
}

// isJSONContentType reports whether ct denotes JSON, tolerating parameters
// such as "application/json; charset=utf-8".
func isJSONContentType(ct string) bool {
	mediaType, _, _ := strings.Cut(ct, ";")
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "application/json", "text/json", "application/problem+json":
		return true
	default:
		return false
	}
}
