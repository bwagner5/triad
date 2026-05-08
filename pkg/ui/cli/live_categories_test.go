package cli

import (
	"context"
	"strings"
	"testing"

	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/spinner"
	"github.com/bwagner5/triad/pkg/runtime"
)

// newLiveModelForTest builds a liveModel without a channel, seeded with
// a slice of runtime.Event so View() can render a deterministic frame.
func newLiveModelForTest(saga string, steps []runtime.Event) *liveModel {
	return &liveModel{
		ctx:   context.Background(),
		sp:    spinner.New(),
		bar:   progress.New(progress.WithDefaultBlend(), progress.WithoutPercentage()),
		saga:  saga,
		steps: steps,
		total: len(steps),
		width: 120,
	}
}

// stripANSIInternal duplicates the same helper used by cli_test but
// lives in-package to avoid cross-package imports.
func stripANSIInternal(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		if r == 0x1b {
			in = true
			continue
		}
		if in {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				in = false
			}
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// TestLiveRendererInsertsCategoryDividers asserts the live renderer
// emits a muted `── <Category> ──` divider when the category changes
// between consecutive events, and indents step rows two spaces under
// the divider.
func TestLiveRendererInsertsCategoryDividers(t *testing.T) {
	m := newLiveModelForTest("deploy", []runtime.Event{
		{Category: "Plan", Step: "p1", Status: runtime.OK},
		{Category: "Plan", Step: "p2", Status: runtime.OK},
		{Category: "Provision", Step: "q1", Status: runtime.Running},
	})
	view := m.View()
	out := stripANSIInternal(view.Content)

	if !strings.Contains(out, "── Plan ──") {
		t.Errorf("missing Plan divider:\n%s", out)
	}
	if !strings.Contains(out, "── Provision ──") {
		t.Errorf("missing Provision divider:\n%s", out)
	}
	if n := strings.Count(out, "── Plan ──"); n != 1 {
		t.Errorf("Plan divider printed %d times, want 1:\n%s", n, out)
	}

	// Step lines under the divider should be indented by two spaces.
	lines := strings.Split(out, "\n")
	// Find a line containing "p1" and ensure it starts with "  ".
	foundIndented := false
	for _, ln := range lines {
		if strings.Contains(ln, "p1") {
			if !strings.HasPrefix(ln, "  ") {
				t.Errorf("categorized step row must be indented, got %q", ln)
			} else {
				foundIndented = true
			}
			break
		}
	}
	if !foundIndented {
		t.Errorf("p1 row not found in view:\n%s", out)
	}
}

// TestLiveRendererFlatWhenNoCategories verifies an ungrouped saga
// renders without a divider and without extra indentation, so short
// workflows keep today's behavior byte-for-byte.
func TestLiveRendererFlatWhenNoCategories(t *testing.T) {
	m := newLiveModelForTest("delete", []runtime.Event{
		{Step: "stop", Status: runtime.OK},
		{Step: "remove", Status: runtime.Running},
	})
	view := m.View()
	out := stripANSIInternal(view.Content)

	if strings.Contains(out, "──") {
		t.Errorf("flat saga must not print divider; got:\n%s", out)
	}
	// Step rows flush-left. Skip the preexisting "working · <label>"
	// line that the live renderer always prints under the progress bar
	// for the currently-running step — it's a muted status indicator,
	// not a step row, and it's indented for legibility regardless of
	// whether categories are in use.
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(ln, "working ·") {
			continue
		}
		if strings.Contains(ln, "stop") || strings.Contains(ln, "remove") {
			if strings.HasPrefix(ln, "  ") {
				t.Errorf("flat step row must not be indented, got %q", ln)
			}
		}
	}
}

// TestLiveRendererCategoryReEntryPrintsTwice pins the plan's
// consecutive-only grouping rule for the CLI side: a category that
// re-enters (A → ungrouped → A) prints its divider twice.
func TestLiveRendererCategoryReEntryPrintsTwice(t *testing.T) {
	m := newLiveModelForTest("deploy", []runtime.Event{
		{Category: "A", Step: "a1", Status: runtime.OK},
		{Step: "flat", Status: runtime.OK}, // ungrouped
		{Category: "A", Step: "a2", Status: runtime.Running},
	})
	view := m.View()
	out := stripANSIInternal(view.Content)
	if n := strings.Count(out, "── A ──"); n != 2 {
		t.Errorf("A divider count = %d, want 2 (initial + re-entry); got:\n%s", n, out)
	}
}
