package config

import (
	"io"
	"log/slog"
	"strings"
)

// NewLogger builds the process-wide structured logger from LogConfig.
// `format=text` produces human-readable output for local development;
// `format=json` produces the machine-parseable form used in production.
func NewLogger(cfg LogConfig, w io.Writer) *slog.Logger {
	opts := &slog.HandlerOptions{
		Level:     parseLevel(cfg.Level),
		AddSource: cfg.AddSource,
	}

	var handler slog.Handler
	if strings.EqualFold(cfg.Format, "text") {
		handler = slog.NewTextHandler(w, opts)
	} else {
		handler = slog.NewJSONHandler(w, opts)
	}
	return slog.New(handler)
}

// parseLevel maps the configured level name onto slog's levels, defaulting to
// info for unrecognised values.
func parseLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
