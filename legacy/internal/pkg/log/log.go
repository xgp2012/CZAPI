// Package log provides the gateway's structured logger (slog based).
//
// Every log line carries request_id / key_id / account_id when available so a
// single request can be traced end to end, per plan §6.9.
package log

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

// Logger is a thin wrapper that keeps call sites short.
type Logger struct{ *slog.Logger }

type ctxKey struct{}

// New builds a logger for the given level and format (json|text).
func New(level, format string) *Logger {
	var lv slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lv = slog.LevelDebug
	case "warn", "warning":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lv}
	var h slog.Handler
	if strings.EqualFold(format, "text") {
		h = slog.NewTextHandler(os.Stdout, opts)
	} else {
		h = slog.NewJSONHandler(os.Stdout, opts)
	}
	return &Logger{slog.New(h)}
}

// WithContext returns the logger stored in ctx, or the fallback logger.
func WithContext(ctx context.Context, fallback *Logger) *Logger {
	if l, ok := ctx.Value(ctxKey{}).(*Logger); ok && l != nil {
		return l
	}
	return fallback
}

// IntoContext stores l in ctx.
func IntoContext(ctx context.Context, l *Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

// Discard returns a logger that drops everything (useful in tests).
func Discard() *Logger {
	return &Logger{slog.New(slog.NewTextHandler(discardWriter{}, &slog.HandlerOptions{Level: slog.LevelError + 1}))}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
