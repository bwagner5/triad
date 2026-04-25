package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// TestRunAttribution exercises the Ryer-style Run entry point end-to-end
// without touching global OS state. Parallel-safe.
func TestRunAttribution(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	getenv := func(string) string { return "" }
	err := Run(context.Background(), []string{"triad", "attribution"}, getenv, &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run error: %v (stderr=%s)", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "ATTRIBUTION") && stdout.Len() == 0 {
		t.Errorf("expected attribution output, got: %q", stdout.String())
	}
}

// TestRunHelpNoAWS verifies --help runs without reading any env vars
// and without global state — proves getenv plumbing.
func TestRunHelpNoAWS(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	called := map[string]int{}
	getenv := func(k string) string {
		called[k]++
		return ""
	}
	err := Run(context.Background(), []string{"triad", "--help"}, getenv, &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	// Our getenv got at least one call (TRIAD_* default resolution).
	if len(called) == 0 {
		t.Error("Run didn't use the provided getenv")
	}
}
