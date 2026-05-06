// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// Package trace is triad's logging vocabulary: a process-global level
// variable, a context-carried *slog.Logger, and a handful of helpers.
//
// Triad is a library. File handling, redaction, retention, and handler
// construction live in the embedder (e.g. lightsailctl's
// internal/logging package). This package deliberately does NOT import
// os, io, or time for handler work, and never opens a file.
//
// Usage from call sites:
//
//	log := trace.FromContext(ctx)
//	log.InfoContext(ctx, "deploy started", "app", appName, "env", env)
//	trace.Trace(ctx, "ignore patterns resolved", "count", n)
//
// Usage from an embedder (lightsailctl):
//
//	h := slog.NewTextHandler(sink, &slog.HandlerOptions{Level: trace.LevelVar})
//	logger := slog.New(h)
//	slog.SetDefault(logger)
//	ctx = trace.IntoContext(ctx, logger)
//	if debug {
//	    trace.SetLevel(trace.LevelTrace)
//	}
package trace

import (
	"context"
	"io"
	"log/slog"
)

// LevelTrace is a level below slog.LevelDebug for per-step, per-retry,
// per-API-call verbose diagnostics. slog.LevelDebug is -4, so -8
// leaves headroom between the two.
const LevelTrace slog.Level = -8

// LevelVar is the process-global threshold. Embedders install it as the
// Leveler on their slog.Handler chain so SetLevel can flip verbosity
// atomically at runtime. It defaults to slog.LevelInfo.
//
// Call sites never touch LevelVar directly — they call SetLevel.
var LevelVar = new(slog.LevelVar)

// SetLevel atomically changes the process-global threshold. Safe to
// call from any goroutine (slog.LevelVar.Set is atomic under the hood).
func SetLevel(l slog.Level) { LevelVar.Set(l) }

// ResetForTest restores the defaults a test may have mutated: LevelVar
// to Info, slog.Default to the stdlib default. Tests that mutate
// either must register t.Cleanup(trace.ResetForTest).
func ResetForTest() {
	LevelVar.Set(slog.LevelInfo)
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// loggerKey is the context key for the attached *slog.Logger.
type loggerKey struct{}

// discardLogger is returned by FromContext when no logger is attached.
// Discard (rather than slog.Default) is the library-safe fallback:
// triad tests without a configured logger produce no output.
var discardLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

// IntoContext attaches l to ctx. Subsequent FromContext calls on this
// ctx (and any child context) return l.
func IntoContext(ctx context.Context, l *slog.Logger) context.Context {
	if l == nil {
		return ctx
	}
	return context.WithValue(ctx, loggerKey{}, l)
}

// FromContext returns the logger attached to ctx, or a discard logger
// if none is attached. The returned logger is never nil.
func FromContext(ctx context.Context) *slog.Logger {
	if ctx == nil {
		return discardLogger
	}
	if l, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok && l != nil {
		return l
	}
	return discardLogger
}

// WithUI returns a ctx whose attached logger has a "ui" attribute. The
// four UIs are "cli", "interactive", "tui", and "watch"; set this at
// the top of each UI's Run entrypoint so every downstream log line
// inherits it.
func WithUI(ctx context.Context, name string) context.Context {
	return WithAttrs(ctx, slog.String("ui", name))
}

// WithAttrs returns a ctx whose attached logger has the given base
// attrs added. The saga runtime uses this to stamp resource/op/step
// attrs on every step's ctx before calling the step's Do func.
func WithAttrs(ctx context.Context, attrs ...slog.Attr) context.Context {
	if len(attrs) == 0 {
		return ctx
	}
	args := make([]any, 0, len(attrs))
	for _, a := range attrs {
		args = append(args, a)
	}
	l := FromContext(ctx).With(args...)
	return IntoContext(ctx, l)
}

// Trace logs at LevelTrace. slog.Logger has no .Trace() method, so
// this is the convenience verb every TRACE call site uses. args are
// slog-style (key, value, key, value, …) or slog.Attr values.
func Trace(ctx context.Context, msg string, args ...any) {
	FromContext(ctx).Log(ctx, LevelTrace, msg, args...)
}
