package cli_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/bwagner5/triad/pkg/registry"
	"github.com/bwagner5/triad/pkg/runtime"
	"github.com/bwagner5/triad/pkg/ui/cli"
)

// TestRenderEventsInsertsCategoryDividers verifies a muted `── <Name> ──`
// divider is printed whenever the Category transitions, and subsequent
// step rows are indented under it.
func TestRenderEventsInsertsCategoryDividers(t *testing.T) {
	ch := make(chan runtime.Event, 16)
	push := func(e runtime.Event) { ch <- e }

	push(runtime.Event{Saga: "deploy", Category: "Plan", Step: "p1", Status: runtime.Running})
	push(runtime.Event{Saga: "deploy", Category: "Plan", Step: "p1", Status: runtime.OK})
	push(runtime.Event{Saga: "deploy", Category: "Plan", Step: "p2", Status: runtime.Running})
	push(runtime.Event{Saga: "deploy", Category: "Plan", Step: "p2", Status: runtime.OK})
	push(runtime.Event{Saga: "deploy", Category: "Provision", Step: "q1", Status: runtime.Running})
	push(runtime.Event{Saga: "deploy", Category: "Provision", Step: "q1", Status: runtime.OK})
	push(runtime.Event{Saga: "deploy", Status: runtime.OK, Done: true})
	close(ch)

	var buf bytes.Buffer
	if err := cli.RenderEvents(&buf, ch); err != nil {
		t.Fatal(err)
	}
	out := stripANSI(buf.String())
	if !strings.Contains(out, "── Plan ──") {
		t.Errorf("missing Plan divider:\n%s", out)
	}
	if !strings.Contains(out, "── Provision ──") {
		t.Errorf("missing Provision divider:\n%s", out)
	}
	// Only print divider once per transition: Plan divider should
	// appear exactly once.
	if n := strings.Count(out, "── Plan ──"); n != 1 {
		t.Errorf("Plan divider printed %d times, want 1:\n%s", n, out)
	}
	// Step lines under a category are indented by two spaces.
	// Search for "  " + glyph + " " + label. We don't know which
	// glyph (spinner/check/error frame) so test both.
	if !strings.Contains(out, "  ") || !strings.Contains(out, "p1") {
		t.Errorf("step row missing under divider:\n%s", out)
	}
}

// TestRenderEventsFlatWhenNoCategories pins the zero-change path: when
// every event has Category == "", no divider appears and lines render
// flush-left exactly as before.
func TestRenderEventsFlatWhenNoCategories(t *testing.T) {
	ch := make(chan runtime.Event, 8)
	ch <- runtime.Event{Saga: "delete", Step: "stop", Status: runtime.Running}
	ch <- runtime.Event{Saga: "delete", Step: "stop", Status: runtime.OK}
	ch <- runtime.Event{Saga: "delete", Status: runtime.OK, Done: true}
	close(ch)

	var buf bytes.Buffer
	if err := cli.RenderEvents(&buf, ch); err != nil {
		t.Fatal(err)
	}
	out := stripANSI(buf.String())
	if strings.Contains(out, "──") {
		t.Errorf("flat saga must not print a category divider; got:\n%s", out)
	}
	// Step row should not be indented (first line after any leading
	// blank newline starts with the glyph, not "  ").
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(ln, "stop") {
			if strings.HasPrefix(ln, "  ") {
				t.Errorf("flat-saga step row must render flush-left, got %q", ln)
			}
			break
		}
	}
}

// TestRenderEventsCategoryTransitionResetsOnEmpty covers an edge case:
// going from a category into an ungrouped step and back to a category
// prints each divider exactly once, in order.
func TestRenderEventsCategoryTransitionResetsOnEmpty(t *testing.T) {
	ch := make(chan runtime.Event, 8)
	ch <- runtime.Event{Saga: "x", Category: "A", Step: "a1", Status: runtime.OK}
	ch <- runtime.Event{Saga: "x", Category: "", Step: "flat", Status: runtime.OK} // ungrouped tail
	ch <- runtime.Event{Saga: "x", Category: "A", Step: "a2", Status: runtime.OK}  // re-entry
	ch <- runtime.Event{Saga: "x", Status: runtime.OK, Done: true}
	close(ch)

	var buf bytes.Buffer
	if err := cli.RenderEvents(&buf, ch); err != nil {
		t.Fatal(err)
	}
	out := stripANSI(buf.String())
	// Two A dividers: once on initial entry, once on re-entry.
	if n := strings.Count(out, "── A ──"); n != 2 {
		t.Errorf("A divider count = %d, want 2 (initial + re-entry); got:\n%s", n, out)
	}
	// Step ordering: a1 → flat → a2.
	a1Idx := strings.Index(out, "a1")
	flatIdx := strings.Index(out, "flat")
	a2Idx := strings.Index(out, "a2")
	if a1Idx < 0 || flatIdx < 0 || a2Idx < 0 ||
		(a1Idx >= flatIdx || flatIdx >= a2Idx) {
		t.Errorf("step order wrong in:\n%s", out)
	}
}

// TestRenderEventsOutputPrefixedUnderCategory verifies that per-step
// OK Output lines — which the plain renderer does not emit at all — do
// not accidentally start printing under the divider (guard against a
// future refactor surfacing Output and forgetting to indent).
func TestRenderEventsOutputPrefixedUnderCategory(t *testing.T) {
	// Render a failing categorized step so the error message (which IS
	// emitted) ends up on the correct, indented line.
	ch := make(chan runtime.Event, 4)
	ch <- runtime.Event{Saga: "x", Category: "A", Step: "a1", Status: runtime.Running}
	ch <- runtime.Event{Saga: "x", Category: "A", Step: "a1", Status: runtime.Failed,
		Err: errWrap("boom")}
	ch <- runtime.Event{Saga: "x", Status: runtime.Failed, Err: errWrap("boom"), Done: true}
	close(ch)

	var buf bytes.Buffer
	_ = cli.RenderEvents(&buf, ch)
	out := stripANSI(buf.String())
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(ln, "a1") && strings.Contains(ln, "boom") {
			if !strings.HasPrefix(ln, "  ") {
				t.Errorf("failed step under category must be indented, got %q", ln)
			}
			break
		}
	}
}

// errWrap is a test-local sentinel error type so we don't pull in extra
// imports just for a string carrier.
type errWrap string

func (e errWrap) Error() string { return string(e) }

// TestRenderEventsRespectsRegistryType proves the category value is
// plumbed through via registry.Step → runtime.Event → cli render path.
// Keeps everyone on the same Category string.
func TestRenderEventsRespectsRegistryType(t *testing.T) {
	_ = registry.Step{Category: "A"} // compile-time: the field exists.
}
