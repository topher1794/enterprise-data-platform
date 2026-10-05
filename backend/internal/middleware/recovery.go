package middleware

import (
	"context"
	"errors"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/edp/edp-control-plane/internal/platform"
)

// Recovery converts a panic in a downstream handler into a 500 JSON response,
// so a single bad request cannot terminate the process or leak a Go stack
// trace to the client. The panic and its stack are logged at ERROR.
func Recovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}

			// http.ErrAbortHandler is the documented way to abandon a response
			// without a stack trace; re-panic so net/http can handle it.
			if err, ok := recovered.(error); ok && err == http.ErrAbortHandler {
				panic(recovered)
			}

			log.ErrorContext(r.Context(), "recovered from panic",
				"panic", recovered,
				"method", r.Method,
				"path", r.URL.Path,
				"request_id", platform.RequestIDFrom(r.Context()),
				"stack", string(debug.Stack()),
			)

			// The handler may already have written a partial response, in which
			// case there is nothing safe left to send.
			if rec, ok := w.(*responseRecorder); ok && rec.status != 0 {
				log.WarnContext(r.Context(), "cannot send error response; headers already written",
					"status", rec.status,
					"request_id", platform.RequestIDFrom(r.Context()),
				)
				return
			}

			platform.Fail(w, r, platform.NewInternal("the server encountered an unexpected error"))
		}()

		next.ServeHTTP(w, r)
	})
}

// Timeout bounds how long a request may take. It wraps the response writer and,
// on expiry, switches writes to 503 while releasing the handler goroutine via
// its context.
//
// It deliberately does not use http.TimeoutHandler: that buffers the entire
// response body, which is incompatible with streaming endpoints and wastes
// memory proportional to response size.
func Timeout(d time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if d <= 0 {
			return next
		}

		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()

			tw := &timeoutWriter{ResponseWriter: w}

			// Serve on a separate goroutine so an unresponsive handler does not
			// block the timeout path.
			done := make(chan struct{})
			panicked := make(chan any, 1)

			go func() {
				defer func() {
					if rec := recover(); rec != nil {
						panicked <- rec
					}
					close(done)
				}()
				next.ServeHTTP(tw, r.WithContext(ctx))
			}()

			select {
			case rec := <-panicked:
				// Re-panic on the request goroutine so Recovery handles it
				// exactly as it would a synchronous panic.
				panic(rec)

			case <-ctx.Done():
				if errors.Is(ctx.Err(), context.DeadlineExceeded) {
					tw.expire()
					log.WarnContext(r.Context(), "request exceeded its time budget",
						"method", r.Method,
						"path", r.URL.Path,
						"timeout", d.String(),
						"request_id", platform.RequestIDFrom(r.Context()),
					)
					platform.Fail(tw, r, platform.NewInternal(
						"the request exceeded the %s time limit", d.String()))
				}

			case <-done:
			}

			// Give the handler a brief window to notice the cancelled context
			// and unwind, so the connection is not torn out from under it.
			select {
			case <-done:
			case <-time.After(100 * time.Millisecond):
				log.WarnContext(r.Context(), "handler still running after timeout",
					"method", r.Method,
					"path", r.URL.Path,
					"timeout", d.String(),
				)
			}
		})
	}
}

// timeoutWriter forwards writes until the deadline passes, after which it
// refuses further writes so a late handler cannot corrupt the 503 response.
type timeoutWriter struct {
	http.ResponseWriter
	expired bool
}

func (w *timeoutWriter) expire() { w.expired = true }

func (w *timeoutWriter) WriteHeader(status int) {
	if w.expired {
		return
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *timeoutWriter) Write(b []byte) (int, error) {
	if w.expired {
		return 0, http.ErrHandlerTimeout
	}
	return w.ResponseWriter.Write(b)
}

// Flush forwards to the underlying writer so SSE and streaming handlers keep
// working when wrapped.
func (w *timeoutWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// MaxBodyBytes limits the request body a handler will read, rejecting anything
// larger with 413 before decoding.
func MaxBodyBytes(limit int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if limit <= 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > limit {
				platform.Fail(w, r, platform.NewBadRequest(
					"request body exceeds the %d byte limit", limit))
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
			next.ServeHTTP(w, r)
		})
	}
}

// CORS applies the configured cross-origin policy. An empty allow-list means
// same-origin only: no CORS headers are emitted at all, which is the safest
// default for a credentialed API.
func CORS(allowedOrigins []string) func(http.Handler) http.Handler {
	allowAll := false
	allowed := make(map[string]struct{}, len(allowedOrigins))

	for _, origin := range allowedOrigins {
		trimmed := strings.TrimSpace(strings.ToLower(origin))
		switch {
		case trimmed == "*":
			allowAll = true
		case trimmed != "":
			allowed[trimmed] = struct{}{}
		}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := strings.ToLower(strings.TrimSpace(r.Header.Get("Origin")))

			permitted := allowAll && origin != ""
			if _, ok := allowed[origin]; ok {
				permitted = true
			}

			if permitted {
				// Reflect the specific origin rather than "*", because the API
				// accepts bearer tokens and must never be wildcard-credentialed.
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Add("Vary", "Origin")
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers",
					"Authorization, Content-Type, "+platform.RequestIDHeader)
				w.Header().Set("Access-Control-Expose-Headers", platform.RequestIDHeader)
				w.Header().Set("Access-Control-Max-Age", "600")
			}

			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
