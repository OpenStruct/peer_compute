// Package logging provides a structured logger factory using log/slog.
package logging

import (
	"log/slog"
	"os"
	"strings"
)

// New creates a slog.Logger tagged with the given component name.
// Log level is controlled by LOG_LEVEL env var (debug/info/warn/error, default: info).
// Format is controlled by LOG_FORMAT env var (json/text, default: text).
func New(component string) *slog.Logger {
	level := parseLevel(os.Getenv("LOG_LEVEL"))

	opts := &slog.HandlerOptions{Level: level}

	var handler slog.Handler
	if strings.EqualFold(os.Getenv("LOG_FORMAT"), "json") {
		handler = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		handler = slog.NewTextHandler(os.Stderr, opts)
	}

	return slog.New(handler).With("component", component)
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
