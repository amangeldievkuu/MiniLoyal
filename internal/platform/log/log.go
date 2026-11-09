// Package log configures the process-wide slog logger.
package log

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// Setup installs a logger writing to w and returns it. format is "json"
// (production) or "text" (development); level is debug, info, warn or error.
func Setup(w io.Writer, level, format string) (*slog.Logger, error) {
	lvl, err := ParseLevel(level)
	if err != nil {
		return nil, err
	}
	opts := &slog.HandlerOptions{Level: lvl}
	var h slog.Handler
	switch strings.ToLower(format) {
	case "json", "":
		h = slog.NewJSONHandler(w, opts)
	case "text":
		h = slog.NewTextHandler(w, opts)
	default:
		return nil, fmt.Errorf("log: unknown format %q (want json or text)", format)
	}
	logger := slog.New(h)
	slog.SetDefault(logger)
	return logger, nil
}

// ParseLevel converts a level name to a slog.Level.
func ParseLevel(s string) (slog.Level, error) {
	var l slog.Level
	if err := l.UnmarshalText([]byte(strings.ToUpper(strings.TrimSpace(s)))); err != nil {
		return 0, fmt.Errorf("log: unknown level %q", s)
	}
	return l, nil
}
