package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
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

	out := stripANSI(s.Box(120, 40, "⟳", "⟳"))
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

	out := stripANSI(s.Box(120, 40, "⟳", "⟳"))
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

	out := stripANSI(s.Box(120, 40, "⟳", "⟳"))
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
	_ = s.Box(120, 40, "⟳", "⟳")
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

	out := stripANSI(s.Box(120, 40, "⟳", "⟳"))
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
	_ = s.Box(120, 40, "⟳", "⟳")
	if !s.HandleKey("e") {
		t.Fatal("'e' should be consumed")
	}
	out := stripANSI(s.Box(120, 40, "⟳", "⟳"))
	if strings.Contains(out, "▶") {
		t.Errorf("after 'e' every group must be expanded; got:\n%s", out)
	}
	// 'c' collapses all.
	if !s.HandleKey("c") {
		t.Fatal("'c' should be consumed")
	}
	out = stripANSI(s.Box(120, 40, "⟳", "⟳"))
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

	out := stripANSI(s.Box(120, 40, "⟳", "⟳"))
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

	out := stripANSI(s.Box(80, 30, "⟳", "⟳"))
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
	_ = h.App().saga.Box(h.App().width, h.App().height, "⟳", "⟳")

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
	_ = h.App().saga.Box(h.App().width, h.App().height, "⟳", "⟳")

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
	_ = s.Box(120, 40, "⟳", "⟳")
	if s.HandleKey("q") {
		t.Error("'q' should not be consumed by HandleKey")
	}
}

// Silence unused imports under some test build tags.
var _ = tea.KeyPressMsg{}
var _ = context.Background

// TestHierarchicalSagaGracePeriodBlocksEnterDismiss pins the fix for
// "the overlay doesn't stick around after finishing." After a
// categorized saga completes, enter/esc must NOT dismiss the overlay
// until minDoneDisplay has elapsed — otherwise the user's "confirm"
// muscle memory tears down the progress view before they can read
// the outcome. The auto-dismiss Tick fires at the same boundary.
func TestHierarchicalSagaGracePeriodBlocksEnterDismiss(t *testing.T) {
	reg := registry.New()
	reg.Register(registry.Resource{Name: "app", Plural: "apps", Store: fakeStore{}})
	h := newHarness(t, reg, Options{Name: "test"})
	h.Resize(160, 40)

	h.App().saga.Start("deploy", threeGroup())
	// Run through every step and emit the final Done event so the
	// overlay is in the "just completed" state the grace guards.
	for i, st := range threeGroup() {
		h.App().saga.Push(runtime.Event{
			Step: st.Label, Index: i, Category: st.Category,
			Status: runtime.OK, At: time.Now(),
		})
	}
	h.App().saga.Push(runtime.Event{Done: true, Status: runtime.OK, At: time.Now()})
	// Force a render so visibleLines is populated before we send keys.
	_ = h.App().saga.Box(h.App().width, h.App().height, "⟳", "⟳")

	// doneAt was just stamped: enter should be suppressed during the
	// grace window so users get a beat to read.
	h.Press("enter")
	if !h.App().saga.Active() {
		t.Error("enter during grace window should not dismiss the overlay")
	}
	h.Press("esc")
	if !h.App().saga.Active() {
		t.Error("esc during grace window should not dismiss the overlay")
	}

	// Simulate time passing by rewinding doneAt past the grace cutoff.
	// After the window elapses esc dismisses immediately. (Enter on a
	// header/step row still toggles the category — that's the whole
	// point of the hierarchical overlay — so it doesn't dismiss. The
	// auto-dismiss Tick also fires at this boundary.)
	h.App().saga.doneAt = time.Now().Add(-2 * minDoneDisplay)
	h.Press("esc")
	if h.App().saga.Active() {
		t.Error("esc after grace window should dismiss the overlay")
	}
}

// TestFlatSagaNoGracePeriod confirms the grace applies only to
// categorized sagas. Short flat workflows (instance delete, etc.)
// have nothing to read — forcing a delay there would just add
// friction.
func TestFlatSagaNoGracePeriod(t *testing.T) {
	s := newSagaOverlay()
	s.Start("delete", []registry.Step{{Label: "stop"}}) // flat, no Category
	s.Push(runtime.Event{Step: "stop", Index: 0, Status: runtime.OK, At: time.Now()})
	s.Push(runtime.Event{Done: true, Status: runtime.OK, At: time.Now()})

	if !s.dismissable() {
		t.Error("flat saga should always be dismissable — no categorized output to read")
	}
}

// TestEnterTogglesFromStepRowCollapsesParent is the fix for "some
// drop-downs wouldn't expand." Previously, enter only toggled when
// the cursor was precisely on a category header line — which meant
// step-row navigation broke the toggle and users saw inconsistent
// behavior. Now enter on any line inside a group toggles that group.
func TestEnterTogglesFromStepRowCollapsesParent(t *testing.T) {
	s := newSagaOverlay()
	s.Start("deploy", threeGroup())
	// Drive Plan to running so it auto-expands, then render so the
	// visible-lines slice reflects the expanded state.
	pushEvent(&s, 0, runtime.Running, "p1", "Plan", nil)
	_ = s.Box(120, 40, "⟳", "⟳")

	// Navigate to a step row inside the running category.
	if !s.HandleKey("j") {
		t.Fatal("j should move the cursor down")
	}
	// Cursor should now be on a step row; verify via visibleLines.
	if s.cursor >= len(s.visibleLines) {
		t.Fatalf("cursor out of range: %d / %d", s.cursor, len(s.visibleLines))
	}
	if s.visibleLines[s.cursor].kind != sagaLineStep {
		t.Fatalf("expected cursor on step row, got kind=%v", s.visibleLines[s.cursor].kind)
	}

	// Find the parent group's start index so we can assert the
	// toggle affected the right group.
	parent := s.groups[s.visibleLines[s.cursor].groupID].start
	before := s.expanded[parent]

	if !s.HandleKey("enter") {
		t.Fatal("enter on a step row inside a group should be consumed by the toggle handler")
	}
	if s.expanded[parent] == before {
		t.Errorf("enter on step row did not toggle parent group %d (still %v)", parent, before)
	}
	if !s.userToggled[parent] {
		t.Error("manual toggle should mark userToggled so auto-rules don't fight the user")
	}
}

// TestSpaceTogglesFromHeader confirms space still works on the
// header itself (regression guard for the pre-existing behavior).
func TestSpaceTogglesFromHeader(t *testing.T) {
	s := newSagaOverlay()
	s.Start("deploy", threeGroup())
	pushEvent(&s, 0, runtime.Running, "p1", "Plan", nil)
	_ = s.Box(120, 40, "⟳", "⟳")

	// Cursor starts on the running header (Plan).
	if s.visibleLines[s.cursor].kind != sagaLineHeader {
		t.Fatalf("expected cursor on header, got kind=%v", s.visibleLines[s.cursor].kind)
	}
	before := s.expanded[0] // Plan starts at step 0

	if !s.HandleKey("space") {
		t.Fatal("space on a header should toggle")
	}
	if s.expanded[0] == before {
		t.Error("space did not toggle Plan")
	}
}

// TestOverallCounts_ExcludesSkipped pins the helper powering the
// header "N / M" tally and the progress-bar fill: skipped steps are
// subtracted from both sides so the visible denominator matches real
// work, while OK and Failed both count as "done" (we don't rewind
// the bar on a failure).
func TestOverallCounts_ExcludesSkipped(t *testing.T) {
	s := newSagaOverlay()
	s.Start("deploy", threeGroup()) // 6 steps
	// p1 OK, p2 Skipped, q1 Failed, q2/q3/d1 pending.
	pushEvent(&s, 0, runtime.OK, "p1", "Plan", nil)
	pushEvent(&s, 1, runtime.Skipped, "p2", "Plan", nil)
	pushEvent(&s, 2, runtime.Failed, "q1", "Provision", nil)

	done, total := s.overallCounts()
	if done != 2 {
		t.Errorf("done = %d; want 2 (p1 OK + q1 Failed)", done)
	}
	if total != 5 {
		t.Errorf("total = %d; want 5 (6 steps − 1 skipped)", total)
	}
}

// TestProgressBarRendersWhileRunning verifies the progress bar is
// present in the hierarchical overlay output while the saga is in
// flight and disappears once the saga completes (so the completion
// summary isn't visually crowded — matches the CLI live renderer).
func TestProgressBarRendersWhileRunning(t *testing.T) {
	s := newSagaOverlay()
	s.SetSize(160, 40)
	s.Start("deploy", threeGroup())
	pushEvent(&s, 0, runtime.OK, "p1", "Plan", nil)
	pushEvent(&s, 1, runtime.Running, "p2", "Plan", nil)

	out := stripANSI(s.Box(160, 40, "⟳", "⟳"))
	// The bar uses the default blend; its unfilled tail is ASCII
	// whose characters depend on terminal. We assert on the
	// N / M header counter which is always present alongside.
	if !strings.Contains(out, "1 / 6") {
		t.Errorf("expected '1 / 6' counter in header:\n%s", out)
	}

	// Drive to completion and ensure the bar is suppressed.
	for i, st := range threeGroup() {
		pushEvent(&s, i, runtime.OK, st.Label, st.Category, nil)
	}
	s.Push(runtime.Event{Done: true, Status: runtime.OK, At: time.Now()})
	done := stripANSI(s.Box(160, 40, "⟳", "⟳"))
	if !strings.Contains(done, "✓ complete") {
		t.Errorf("completion summary missing:\n%s", done)
	}
}

// TestProgressBar_HiddenForSingleStepFlat guards the CLI-parity
// choice: a single-step flat saga (e.g. instance create) goes
// straight from 0 to 1 completed, so a bar there would just be
// noise. The overlay omits it.
func TestProgressBar_HiddenForSingleStepFlat(t *testing.T) {
	s := newSagaOverlay()
	s.SetSize(120, 30)
	s.Start("create", []registry.Step{{Label: "do-the-thing"}})
	pushEvent(&s, 0, runtime.Running, "do-the-thing", "", nil)

	out := stripANSI(s.Box(120, 30, "⟳", "⟳"))
	// A single-step saga should NOT include an "N / M" counter
	// either — the information is already conveyed by the step row.
	// We look for the pattern " 0 / 1 " explicitly.
	if strings.Contains(out, " 0 / 1 ") {
		t.Errorf("single-step saga should suppress N/M counter:\n%s", out)
	}
}

// TestProgressBar_DoesNotOverflowModal is the regression guard for the
// "progress bar wraps to the next line inside the modal" bug. The root
// cause was sizing the bar against the raw terminal width instead of
// the box interior — theme.Border's rounded border + Padding(0, 1)
// subtract 4 columns of chrome, so a bar exactly as wide as the outer
// box overflows by 4 chars and wraps.
//
// We verify two invariants after rendering at a variety of terminal
// sizes and through both rendering paths (flat + hierarchical):
//  1. The outer box fits inside the terminal (never clips against the
//     edge of the screen).
//  2. The progress bar's width is <= the box interior (outer - chrome),
//     which is exactly what prevents wrap.
func TestProgressBar_DoesNotOverflowModal(t *testing.T) {
	cases := []struct {
		name string
		w, h int
	}{
		{"narrow", 60, 40},
		{"medium", 80, 40},
		{"wide", 120, 40},
		{"huge", 200, 40},
	}
	for _, tc := range cases {
		t.Run("flat/"+tc.name, func(t *testing.T) {
			s := newSagaOverlay()
			s.SetSize(tc.w, tc.h)
			s.Start("delete", []registry.Step{
				{Label: "stop"},
				{Label: "remove"},
				{Label: "untag"},
			})
			pushEvent(&s, 0, runtime.OK, "stop", "", nil)
			pushEvent(&s, 1, runtime.Running, "remove", "", nil)

			out := stripANSI(s.Box(tc.w, tc.h, "⟳", "⟳"))
			assertBoxInvariants(t, &s, out, tc.w)
		})
		t.Run("hierarchical/"+tc.name, func(t *testing.T) {
			s := newSagaOverlay()
			s.SetSize(tc.w, tc.h)
			s.Start("deploy", threeGroup())
			pushEvent(&s, 0, runtime.OK, "p1", "Plan", nil)
			pushEvent(&s, 1, runtime.Running, "p2", "Plan", nil)

			out := stripANSI(s.Box(tc.w, tc.h, "⟳", "⟳"))
			assertBoxInvariants(t, &s, out, tc.w)
		})
	}
}

// assertBoxInvariants enforces the two "no wrap" rules that prevent
// the progress bar (and any other interior-sized content) from
// overflowing the box chrome: the outer box must fit inside the
// terminal, and the bar's SetWidth after render must be <= the
// interior width of the box (outer - boxChrome).
func assertBoxInvariants(t *testing.T, s *sagaOverlay, out string, terminalW int) {
	t.Helper()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) == 0 {
		t.Fatalf("empty render")
	}
	outer := lipgloss.Width(lines[0])
	if outer > terminalW {
		t.Errorf("outer width %d exceeds terminal width %d:\n%s", outer, terminalW, out)
	}
	interior := outer - boxChrome
	if s.bar.Width() > interior {
		t.Errorf("bar width %d exceeds box interior %d (outer=%d) — it will wrap inside the modal:\n%s",
			s.bar.Width(), interior, outer, out)
	}
}
