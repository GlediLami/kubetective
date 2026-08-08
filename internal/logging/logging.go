// Package logging wires the process-wide structured logger (roadmap v1.0
// operational maturity). Logging is OPT-IN: nothing is emitted unless the
// user asks, so CLI output stays unchanged. Enable with:
//
//	--log-format=json   (or =text)   | env KUBETECTIVE_LOG_FORMAT=json|text
//	--log-level=debug                | env KUBETECTIVE_LOG_LEVEL=debug
//
// Logs go to stderr so stdout remains the machine-readable render surface.
package logging

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

// Configure sets the process-wide logger from user options. An empty format
// keeps logging disabled (the CLI's default; preserves the byte-exact
// stdout/stderr contract). Unknown values are ignored with the same effect.
func Configure(format, level string) {
	format = strings.ToLower(format)
	var opts *slog.HandlerOptions
	switch strings.ToLower(level) {
	case "debug":
		opts = &slog.HandlerOptions{Level: slog.LevelDebug}
	case "warn", "warning", "error":
		opts = &slog.HandlerOptions{Level: slog.LevelWarn}
	default:
		opts = &slog.HandlerOptions{Level: slog.LevelInfo}
	}
	var h slog.Handler
	switch format {
	case "json":
		h = slog.NewJSONHandler(os.Stderr, opts)
	case "text":
		h = slog.NewTextHandler(os.Stderr, opts)
	default:
		h = slog.NewTextHandler(io.Discard, opts) // off unless requested
	}
	slog.SetDefault(slog.New(h))
}

// Enabled reports whether structured logging is on (any format).
func Enabled() bool {
	if f := os.Getenv("KUBETECTIVE_LOG_FORMAT"); f != "" {
		return f == "json" || f == "text"
	}
	return false
}

// Discard silences the logger (used by tests and non-CLI surfaces).
func Discard() {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
}