package middleware

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/edp/edp-control-plane/internal/platform"
)

// log is the package-level logger. It is replaced during bootstrap by
// SetLogger so that log records emitted from this package carry the same
// handler and level as the rest of the application.
var log = slog.Default()

// SetLogger installs the application logger for use by this package. Passing
// nil restores the default logger.
func SetLogger(logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	log = logger
	slog.SetDefault(logger)
}

// RequestID ensures every request carries a correlation identifier.
//
// An inbound X-Request-ID is honoured only when it is a well-formed UUID;
// anything else is discarded and replaced, which prevents unbounded or
// attacker-controlled values from reaching logs and downstream traces.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(platform.RequestIDHeader)
		if !platform.IsValidRequestID(id) {
			id = NewRequestID()
		}

		// Echo the identifier so callers can quote it in a bug report.
		w.Header().Set(platform.RequestIDHeader, id)

		ctx := platform.WithRequestID(r.Context(), id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// responseRecorder captures the status code and byte count so the logging
// middleware can report them. It deliberately does not buffer the body, so
// streaming responses pass through untouched.
type responseRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *responseRecorder) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
		w.ResponseWriter.WriteHeader(status)
	}
}

func (w *responseRecorder) Write(b []byte) (int, error) {
	if w.status == 0 {
		// A handler that writes without calling WriteHeader implies 200.
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

// Flush forwards to the underlying writer so SSE and streaming handlers keep
// working when wrapped.
func (w *responseRecorder) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the original writer.
func (w *responseRecorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// statusOrDefault resolves the status to log, defaulting to 200 for handlers
// that never wrote a header or body.
func (w *responseRecorder) statusOrDefault() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

// Logging emits one structured record per completed request, at INFO for
// success and 2xx/3xx, and at ERROR for 5xx and panics.
func Logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &responseRecorder{ResponseWriter: w}

		next.ServeHTTP(rec, r)

		elapsed := time.Since(start)
		status := rec.statusOrDefault()

		attrs := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"status", status,
			"bytes", rec.bytes,
			"duration_ms", elapsed.Milliseconds(),
			"request_id", platform.RequestIDFrom(r.Context()),
			"remote_addr", clientIP(r),
			"user_agent", r.UserAgent(),
		}
		if actor, ok := platform.ActorFrom(r.Context()); ok {
			attrs = append(attrs, "actor", actor.ID)
		}

		switch {
		case status >= http.StatusInternalServerError:
			log.ErrorContext(r.Context(), "request completed", attrs...)
		case status >= http.StatusBadRequest:
			log.WarnContext(r.Context(), "request completed", attrs...)
		default:
			log.InfoContext(r.Context(), "request completed", attrs...)
		}
	})
}

// NewRequestID mints a correlation identifier. Exported for tests and for
// background work that is not triggered by a request.
func NewRequestID() string { return uuid.NewString() }
