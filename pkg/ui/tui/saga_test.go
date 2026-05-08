package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/bwagner5/triad/pkg/registry"
	"github.com/bwagner5/triad/pkg/runtime"
)

// ---- saga overlay hierarchical rendering ----
//
// These tests drive sagaOverlay directly (no harness) to exercise the
// per-category state machine: auto-expand on running/failed, auto-collapse
// on ok, user toggle stickiness, expand-all/collapse-all, mixed categorized
// + uncategorized rendering, and the legacy flat render for ungrouped sagas.

// threeGroup returns a Steps skeleton with three consecutive-run
// categories: Plan (2), Provision (3), Deploy (1). Used by most
// per-category tests below.
func threeGroup() []registry.Step {
	return []registry.Step{
		{Category: "Plan", Label: "p1"},
		{Category: "Plan", Label: "p2"},
		{Category: "Provision", Label: "q1"},
		{Category: "Provision", Label: "q2"},
		{Category: "Provision", Label: "q3"},
		{Category: "Deploy", Label: "d1"},
	}
}

// push is a local helper that feeds a sagaOverlay one event at a time.
func pushEvent(s *sagaOverlay, idx int, status runtime.Status, label, cat string, err error) {
	s.Push(runtime.Event{
		Step: label, Index: idx, Category: cat,
		Status: status, At: time.Now(), Err: err,
	})
}

// TestCategoryAutoExpandsOnRunning verifies a category auto-expands the
// first time any member step enters Running, revealing its step rows.
func TestCategoryAutoExpandsOnRunning(t *testing.T) {
	s := newSagaOverlay()
	s.Start("deploy", threeGroup())
	pushEvent(&s, 0, runtime.Running, "p1", "Plan", nil)

	out := stripANSI(s.Box(120, 40, "⟳"))
	if !strings.Contains(out, "▼") {
		t.Errorf("expected an expanded chevron, got:\n%s", out)
	}
	if !strings.Contains(out, "Plan") {
		t.Errorf("header missing:\n%s", out)
	}
	if !strings.Contains(out, "p1") {
		t.Errorf("step row should be visible under running category:\n%s", out)
	}
	// Provision / Deploy haven't started — should be collapsed chevrons.
	if !strings.Contains(out, "▶ ") {
		t.Errorf("expected collapsed chevrons for pending groups:\n%s", out)
	}
}

// TestCategoryAutoCollapsesOnOK verifies a category collapses automatically
// once every non-skipped member has completed OK.
func TestCategoryAutoCollapsesOnOK(t *testing.T) {
	s := newSagaOverlay()
	s.Start("deploy", threeGroup())
	// Plan: run both to OK.
	pushEvent(&s, 0, runtime.Running, "p1", "Plan", nil)
	pushEvent(&s, 0, runtime.OK, "p1", "Plan", nil)
	pushEvent(&s, 1, runtime.Running, "p2", "Plan", nil)
	pushEvent(&s, 1, runtime.OK, "p2", "Plan", nil)

	out := stripANSI(s.Box(120, 40, "⟳"))
	// Plan header exists and is collapsed (▶) — its step rows
	// (p1, p2) must NOT appear under an expanded section.
	planLine := ""
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(ln, "Plan") {
			planLine = ln
			break
		}
	}
	if planLine == "" {
		t.Fatalf("Plan header missing:\n%s", out)
	}
	if !strings.Contains(planLine, "▶") {
		t.Errorf("completed Plan should auto-collapse to ▶; line was:\n%s\nfull:\n%s", planLine, out)
	}
	if strings.Contains(out, "\np1\n") || strings.Contains(out, "  p1 ") {
		// p1 is the step label; it should not appear anywhere outside a
		// collapsed group. Be tolerant of styling whitespace.
		t.Errorf("collapsed Plan must not render step rows, but p1 still present:\n%s", out)
	}
}

// TestCategoryAutoExpandsOnFailure verifies a step failure drives its
// category to "failed" and auto-expands (even if previously collapsed)
// with the error rendered inline under the step label.
func TestCategoryAutoExpandsOnFailure(t *testing.T) {
	s := newSagaOverlay()
	s.Start("deploy", threeGroup())
	pushEvent(&s, 0, runtime.Running, "p1", "Plan", nil)
	pushEvent(&s, 0, runtime.OK, "p1", "Plan", nil)
	pushEvent(&s, 1, runtime.Running, "p2", "Plan", nil)
	// Fail p2.
	boom := errors.New("kaboom")
	pushEvent(&s, 1, runtime.Failed, "p2", "Plan", boom)

	out := stripANSI(s.Box(120, 40, "⟳"))
	if !strings.Contains(out, "▼") {
		t.Errorf("failed Plan should auto-expand (▼); got:\n%s", out)
	}
	if !strings.Contains(out, "kaboom") {
		t.Errorf("failed step must render its error inline; got:\n%s", out)
	}
}

// TestManualToggleSticks verifies that once the user manually expands a
// completed (ok) category, subsequent state changes in other categories
// don't re-collapse it.
func TestManualToggleSticks(t *testing.T) {
	s := newSagaOverlay()
	s.Start("deploy", threeGroup())
	// Complete Plan fully; it auto-collapses.
	for _, i := range []int{0, 1} {
		pushEvent(&s, i, runtime.Running, "plan-step", "Plan", nil)
		pushEvent(&s, i, runtime.OK, "plan-step", "Plan", nil)
	}
	// Prime visibleLines.
	_ = s.Box(120, 40, "⟳")
	// Place cursor on the Plan header (first non-flat group header is
	// line 0 in the visible list).
	if s.cursor != 0 {
		s.cursor = 0
	}
	// User presses enter to expand.
	if !s.HandleKey("enter") {
		t.Fatal("enter on Plan header should be consumed by HandleKey")
	}
	// Now advance Provision into running; auto-rules fire again.
	pushEvent(&s, 2, runtime.Running, "q1", "Provision", nil)

	out := stripANSI(s.Box(120, 40, "⟳"))
	// Plan stays expanded because userToggled[Plan] is set.
	planLine := ""
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(ln, "Plan") {
			planLine = ln
			break
		}
	}
	if !strings.Contains(planLine, "▼") {
		t.Errorf("manually expanded Plan must stay expanded across subsequent transitions; line was %q\nfull:\n%s", planLine, out)
	}
}

// TestExpandAllCollapseAll verifies 'e' expands every category and 'c'
// collapses every category, and that the toggle sticks so auto-rules
// don't override it.
func TestExpandAllCollapseAll(t *testing.T) {
	s := newSagaOverlay()
	s.Start("deploy", threeGroup())
	// Only Plan in running; others pending — default expanded state is
	// only Plan ▼. Press 'e' and assert all ▼.
	pushEvent(&s, 0, runtime.Running, "p1", "Plan", nil)
	_ = s.Box(120, 40, "⟳")
	if !s.HandleKey("e") {
		t.Fatal("'e' should be consumed")
	}
	out := stripANSI(s.Box(120, 40, "⟳"))
	if strings.Contains(out, "▶") {
		t.Errorf("after 'e' every group must be expanded; got:\n%s", out)
	}
	// 'c' collapses all.
	if !s.HandleKey("c") {
		t.Fatal("'c' should be consumed")
	}
	out = stripANSI(s.Box(120, 40, "⟳"))
	if strings.Contains(out, "▼") {
		t.Errorf("after 'c' every group must be collapsed; got:\n%s", out)
	}
}

// TestMixedCategorizedAndUncategorized verifies an uncategorized step
// renders flush-left between (or after) category groups, without a
// chevron.
func TestMixedCategorizedAndUncategorized(t *testing.T) {
	s := newSagaOverlay()
	s.Start("deploy", []registry.Step{
		{Category: "Plan", Label: "p1"},
		{Category: "Plan", Label: "p2"},
		{Label: "Finalize"}, // uncategorized tail step
	})
	for _, i := range []int{0, 1} {
		pushEvent(&s, i, runtime.Running, "p", "Plan", nil)
		pushEvent(&s, i, runtime.OK, "p", "Plan", nil)
	}
	pushEvent(&s, 2, runtime.Running, "Finalize", "", nil)

	out := stripANSI(s.Box(120, 40, "⟳"))
	// Finalize must appear as a flat top-level row. The easiest check:
	// its line does not start with 2+ leading spaces-plus-chevron like
	// a category header would.
	var finalizeLn string
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(ln, "Finalize") {
			finalizeLn = ln
			break
		}
	}
	if finalizeLn == "" {
		t.Fatalf("Finalize row missing from overlay:\n%s", out)
	}
	if strings.Contains(finalizeLn, "▶") || strings.Contains(finalizeLn, "▼") {
		t.Errorf("uncategorized step should NOT have a chevron; got %q", finalizeLn)
	}
}

// TestUngroupedSagaRendersAsToday pins the backward-compat path: a saga
// with every Category empty renders the legacy flat list (no chevrons,
// no group headers, no nav footer).
func TestUngroupedSagaRendersAsToday(t *testing.T) {
	s := newSagaOverlay()
	s.Start("delete", []registry.Step{
		{Label: "stop"},
		{Label: "remove"},
	})
	pushEvent(&s, 0, runtime.Running, "stop", "", nil)
	pushEvent(&s, 0, runtime.OK, "stop", "", nil)
	pushEvent(&s, 1, runtime.Running, "remove", "", nil)
	pushEvent(&s, 1, runtime.OK, "remove", "", nil)
	s.Push(runtime.Event{Done: true, Status: runtime.OK, At: time.Now()})

	out := stripANSI(s.Box(80, 30, "⟳"))
	for _, forbidden := range []string{"▶", "▼", "j/k navigate"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("ungrouped saga must render as today (no %q); got:\n%s", forbidden, out)
		}
	}
	if !strings.Contains(out, "press esc or enter to close") {
		t.Errorf("legacy dismiss hint missing:\n%s", out)
	}
}

// TestOverlayConsumesNavigationKeys verifies the saga overlay's HandleKey
// intercepts j/k/enter/e/c when categories are present, so those keys
// don't bubble through to the underlying table.
func TestOverlayConsumesNavigationKeys(t *testing.T) {
	reg := registry.New()
	reg.Register(registry.Resource{Name: "app", Plural: "apps", Store: fakeStore{}})
	h := newHarness(t, reg, Options{Name: "test"})
	h.Resize(160, 40)

	// Arm the saga overlay with a categorized skeleton and a running event.
	h.App().saga.Start("deploy", threeGroup())
	h.App().saga.Push(runtime.Event{
		Step: "p1", Index: 0, Category: "Plan",
		Status: runtime.Running, At: time.Now(),
	})
	// Render once to populate visibleLines.
	_ = h.App().saga.Box(h.App().width, h.App().height, "⟳")

	// Snapshot cursor position so we can assert j moves it.
	before := h.App().saga.cursor
	// Send 'j' via the real key path (exercises app.handleKey →
	// saga.HandleKey); must not bubble to the underlying table.
	beforeTableCursor := h.App().cursor
	h.Press("j")
	if h.App().cursor != beforeTableCursor {
		t.Errorf("j key leaked to underlying table: table cursor %d -> %d", beforeTableCursor, h.App().cursor)
	}
	if h.App().saga.cursor == before {
		// We only expect cursor motion when there's somewhere to go,
		// which is true for a multi-group saga.
		t.Errorf("j key should have moved the overlay cursor; stayed at %d", before)
	}
}

// TestFlatSagaEnterDismisses verifies the existing "enter dismisses a
// completed flat saga" path still works — the hierarchical refactor
// must not break it.
func TestFlatSagaEnterDismisses(t *testing.T) {
	reg := registry.New()
	reg.Register(registry.Resource{Name: "app", Plural: "apps", Store: fakeStore{}})
	h := newHarness(t, reg, Options{Name: "test"})
	h.Resize(120, 30)

	// Flat saga: no categories at all.
	h.App().saga.Start("delete", []registry.Step{{Label: "stop"}})
	h.App().saga.Push(runtime.Event{
		Step: "stop", Index: 0, Status: runtime.OK, At: time.Now(),
	})
	h.App().saga.Push(runtime.Event{Done: true, Status: runtime.OK, At: time.Now()})

	h.Press("enter")
	if h.App().saga.Active() {
		t.Errorf("enter on a completed flat saga should dismiss the overlay")
	}
}

// TestCategorizedSagaEscDismisses covers the esc path for hierarchical
// sagas — HandleKey should not consume esc so the overlay-level handler
// can dismiss.
func TestCategorizedSagaEscDismisses(t *testing.T) {
	reg := registry.New()
	reg.Register(registry.Resource{Name: "app", Plural: "apps", Store: fakeStore{}})
	h := newHarness(t, reg, Options{Name: "test"})
	h.Resize(160, 40)
	h.App().saga.Start("deploy", threeGroup())
	_ = h.App().saga.Box(h.App().width, h.App().height, "⟳")

	h.Press("esc")
	if h.App().saga.Active() {
		t.Error("esc must dismiss the hierarchical saga overlay")
	}
}

// ---- sagaGroup splitting ----

// TestBuildSagaGroupsSplitsOnCategoryChange is a unit test of the pure
// grouping helper. Consecutive same-Category steps merge; any change
// (including back to empty) starts a new group.
func TestBuildSagaGroupsSplitsOnCategoryChange(t *testing.T) {
	steps := []registry.Step{
		{Category: "A"},
		{Category: "A"},
		{Category: "B"},
		{Category: ""}, // flat
		{Category: "A"}, // re-entry — new group even though name matches
	}
	got := buildSagaGroups(steps)
	want := []sagaGroup{
		{name: "A", start: 0, end: 1},
		{name: "B", start: 2, end: 2},
		{flat: true, start: 3, end: 3},
		{name: "A", start: 4, end: 4},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d groups, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("group[%d] = %+v; want %+v", i, got[i], want[i])
		}
	}
}

// ---- saga.HandleKey edge cases ----

// TestHandleKeyIgnoresUnknown ensures HandleKey returns false for keys
// it doesn't handle so the outer dismiss path still fires (e.g. 'q').
func TestHandleKeyIgnoresUnknown(t *testing.T) {
	s := newSagaOverlay()
	s.Start("deploy", threeGroup())
	_ = s.Box(120, 40, "⟳")
	if s.HandleKey("q") {
		t.Error("'q' should not be consumed by HandleKey")
	}
}

// Silence unused imports under some test build tags.
var _ = tea.KeyPressMsg{}
var _ = context.Background
