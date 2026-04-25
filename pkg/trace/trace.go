// Package trace is a tiny slog-backed logger triad uses for diagnostics.
//
// Quiet by default: calls are no-ops unless Enable(path) is called. The CLI
// wires this to a --debug flag. Output goes to a file so the TUI's alt-screen
// rendering isn't corrupted by stderr writes.
package trace

import (
	"io"
	"log/slog"
	"os"
	"sync/atomic"
)

var (
	enabled atomic.Bool
	logger  atomic.Pointer[slog.Logger]
	file    atomic.Pointer[os.File]
)

// Enable opens path for appending and routes all subsequent Log calls there.
// Safe to call multiple times; each call replaces the previous destination.
// Returns the opened file so callers can Close it at shutdown.
func Enable(path string) (io.Closer, error) {
	// #nosec G304 -- debug log path comes from an operator-controlled flag.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	l := slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug}))
	logger.Store(l)
	if old := file.Swap(f); old != nil {
		_ = old.Close()
	}
	enabled.Store(true)
	l.Info("trace enabled", "path", path)
	return f, nil
}

// Enabled reports whether trace logging is currently active.
func Enabled() bool { return enabled.Load() }

// Log records a debug event. No-op when disabled. kv pairs are slog-style
// (key, value, key, value, ...).
func Log(msg string, kv ...any) {
	if !enabled.Load() {
		return
	}
	if l := logger.Load(); l != nil {
		l.Debug(msg, kv...)
	}
}
