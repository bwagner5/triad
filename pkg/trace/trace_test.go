// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package trace_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/bwagner5/triad/pkg/trace"
)

// withBufferLogger returns a ctx with a slog.Logger attached that writes
// text records to buf. Level comes from trace.LevelVar so tests can
// exercise SetLevel.
func withBufferLogger(buf *bytes.Buffer) context.Context {
	h := slog.NewTextHandler(buf, &slog.HandlerOptions{Level: trace.LevelVar})
	l := slog.New(h)
	return trace.IntoContext(context.Background(), l)
}

func TestFromContextDiscardFallback(t *testing.T) {
	t.Cleanup(trace.ResetForTest)
	// Not attached: FromContext returns a non-nil discard logger.
	log := trace.FromContext(context.Background())
	if log == nil {
		t.Fatalf("FromContext returned nil")
	}
	// Should be a no-op; this must not panic or block.
	log.InfoContext(context.Background(), "hello")
}

func TestIntoContextAndFromContext(t *testing.T) {
	t.Cleanup(trace.ResetForTest)
	var buf bytes.Buffer
	ctx := withBufferLogger(&buf)
	trace.FromContext(ctx).InfoContext(ctx, "hello", "k", "v")
	if !strings.Contains(buf.String(), "hello") || !strings.Contains(buf.String(), "k=v") {
		t.Errorf("expected hello k=v in buf; got %q", buf.String())
	}
}

func TestWithUIStampsAttr(t *testing.T) {
	t.Cleanup(trace.ResetForTest)
	var buf bytes.Buffer
	ctx := trace.WithUI(withBufferLogger(&buf), "tui")
	trace.FromContext(ctx).InfoContext(ctx, "hi")
	if !strings.Contains(buf.String(), "ui=tui") {
		t.Errorf("expected ui=tui; got %q", buf.String())
	}
}

func TestWithAttrsAccumulates(t *testing.T) {
	t.Cleanup(trace.ResetForTest)
	var buf bytes.Buffer
	ctx := trace.WithAttrs(withBufferLogger(&buf),
		slog.String("a", "1"),
		slog.Int("b", 2),
	)
	ctx = trace.WithAttrs(ctx, slog.String("c", "3"))
	trace.FromContext(ctx).InfoContext(ctx, "hi")
	out := buf.String()
	for _, want := range []string{"a=1", "b=2", "c=3"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in %q", want, out)
		}
	}
}

func TestTraceLevelRoutesAtTraceLevel(t *testing.T) {
	t.Cleanup(trace.ResetForTest)
	var buf bytes.Buffer
	ctx := withBufferLogger(&buf)

	// Default threshold is Info: Trace should be suppressed.
	trace.SetLevel(slog.LevelInfo)
	trace.Trace(ctx, "low-volume")
	if buf.Len() != 0 {
		t.Errorf("Trace emitted under Info threshold; got %q", buf.String())
	}

	// Flip to LevelTrace: Trace should appear.
	trace.SetLevel(trace.LevelTrace)
	trace.Trace(ctx, "hello-trace", "k", "v")
	if !strings.Contains(buf.String(), "hello-trace") {
		t.Errorf("expected hello-trace; got %q", buf.String())
	}
}

func TestSetLevelIsAtomic(t *testing.T) {
	t.Cleanup(trace.ResetForTest)
	trace.SetLevel(slog.LevelError)
	if got := trace.LevelVar.Level(); got != slog.LevelError {
		t.Errorf("LevelVar = %v, want Error", got)
	}
}

func TestResetForTestRestoresDefaults(t *testing.T) {
	trace.SetLevel(slog.LevelError)
	trace.ResetForTest()
	if got := trace.LevelVar.Level(); got != slog.LevelInfo {
		t.Errorf("ResetForTest did not reset LevelVar; got %v", got)
	}
}

func TestWithAttrsNilAttrsNoop(t *testing.T) {
	t.Cleanup(trace.ResetForTest)
	ctx := context.Background()
	if got := trace.WithAttrs(ctx); got != ctx {
		t.Errorf("WithAttrs with no attrs should return the same ctx")
	}
}
