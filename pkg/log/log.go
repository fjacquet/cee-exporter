// Package log initialises the application-wide structured logger using
// log/slog (stdlib, Go 1.21+).  Call Init once at startup before any other
// package logs messages.
package log

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

// Init configures the global slog logger.
//
//   - level:  "debug", "info", "warn", or "error" (default "info")
//   - format: "json" for production, "text" for development (default "text")
func Init(level, format string) {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: lvl}

	var handler slog.Handler
	var w io.Writer = os.Stdout
	if strings.ToLower(format) == "json" {
		handler = slog.NewJSONHandler(w, opts)
	} else {
		handler = slog.NewTextHandler(w, opts)
	}

	slog.SetDefault(slog.New(handler))
}
