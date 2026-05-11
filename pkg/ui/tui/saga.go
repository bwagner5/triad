package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/progress"
	"charm.land/lipgloss/v2"
	"github.com/bwagner5/triad/pkg/registry"
	"github.com/bwagner5/triad/pkg/runtime"
	"github.com/bwagner5/triad/pkg/ui/theme"
)

// sagaOverlay shows live step progress for a running saga.
//
// When the Operation's Steps carry Category values, the overlay renders
// the steps grouped under collapsible headers (chevron + name + counts).
// With no categories — or with every Category empty — the overlay falls
// back to the legacy flat rendering so existing callers see no change.
type sagaOverlay struct {
	active    bool
	name      string
	events    []runtime.Event // keyed by step Index
	done      bool
	err       error
	output    string
	startedAt time.Time
	doneAt    time.Time // set the first time we observe done=true; drives the post-completion grace window
	w, h      int

	// minimized collapses the centered modal into a small status pill in
	// the top-right corner of the TUI. The saga continues running and
	// events keep accumulating; only the rendering changes. Press `-` to
	// toggle (handled in handleSagaOverlayKey). When minimized while
	// done, the pill stays put until the user restores and dismisses —
	// that's by design so a backgrounded saga can't silently disappear.
	minimized bool

	// bar is the overall progress indicator shown under the header
	// while the saga is in flight. Ticks as steps complete (non-
	// skipped OK + Failed / total non-skipped). Mirrors the style
	// used by the CLI live renderer so both UIs feel consistent.
	bar progress.Model

	// Step skeleton recorded by Start(). Groups are derived from the
	// Category field without touching event buffers; when steps is nil
	// (legacy callers) the overlay falls back to flat rendering.
	steps  []registry.Step
	groups []sagaGroup // consecutive-run groupings of steps

	// Per-group UI state, keyed by group.start (a stable step index).
	expanded    map[int]bool   // effective expanded state
	userToggled map[int]bool   // user pressed enter on this group — stops auto-collapse
	seenStatus  map[int]string // last auto-transitioned status for each group

	// Cursor tracks the user's position within the list of visible
	// lines rebuilt each render. visibleLines caches the mapping so
	// key handlers know what line the cursor sits on.
	cursor       int
	cursorManual bool // set when the user pressed j/k — stop auto-following
	visibleLines []sagaLine
}

// sagaGroup is one rendering group: a consecutive run of steps with the
// same non-empty Category, OR a single step with empty Category (flat=true).
type sagaGroup struct {
	name  string // empty when flat == true
	flat  bool
	start int // first step index (inclusive); stable key for UI state
	end   int // last step index (inclusive)
}

// sagaLine is one rendered line in the overlay body. The kind drives
// navigation and key handling — category headers toggle, step rows
// don't, and flat step rows look like headers but behave like step rows.
type sagaLine struct {
	kind    sagaLineKind
	groupID int // sagaGroup index; for header lines and step lines
	stepIdx int // event/step index; -1 for header lines
}

type sagaLineKind int

const (
	sagaLineHeader   sagaLineKind = iota // group header with chevron
	sagaLineStep                         // step row (indented under an expanded group)
	sagaLineFlatStep                     // uncategorized step at top level
)

// boxChrome is the horizontal overhead theme.Border adds to any content
// it renders: 1 column of rounded-border glyph + 1 column of Padding(0,1)
// on each side → 4 columns total. lipgloss.Style.Width sets the outer
// block width, so the actual usable interior is outer - boxChrome; we
// must size interior-sensitive content (progress bar in particular) to
// that interior width, not to the outer width, otherwise it wraps onto
// a second line inside the modal.
const boxChrome = 4

func newSagaOverlay() sagaOverlay {
	return sagaOverlay{
		expanded:    map[int]bool{},
		userToggled: map[int]bool{},
		seenStatus:  map[int]string{},
		bar:         progress.New(progress.WithDefaultBlend(), progress.WithoutPercentage()),
	}
}

func (s *sagaOverlay) SetSize(w, h int) {
	s.w, s.h = w, h
	// Bar width is recomputed per-render against the actual box
	// interior (see renderFlat / renderHierarchical) — sizing it from
	// the raw terminal width here would ignore the modal's border +
	// padding chrome and let the bar overflow onto a second line.
}
func (s *sagaOverlay) Active() bool    { return s.active }
func (s *sagaOverlay) Minimized() bool  { return s.active && s.minimized }
func (s *sagaOverlay) Expanded() bool   { return s.active && !s.minimized }
func (s *sagaOverlay) ToggleMinimize()  { s.minimized = !s.minimized }
func (s *sagaOverlay) Clear() {
	// Preserve size + bar model (keeping the progress.Model alive
	// means we don't re-allocate it per op and the gradient blend
	// stays consistent between runs).
	bar := s.bar
	*s = sagaOverlay{
		w: s.w, h: s.h,
		expanded:    map[int]bool{},
		userToggled: map[int]bool{},
		seenStatus:  map[int]string{},
		bar:         bar,
	}
}

// Start records the operation name and (optionally) its step skeleton.
// Passing the steps enables the hierarchical category view: headers for
// categories that haven't started yet appear as collapsed "pending".
// Callers that don't care (or have a single-step op) may pass nil.
func (s *sagaOverlay) Start(name string, steps []registry.Step) {
	s.active = true
	s.name = name
	s.events = nil
	s.done = false
	s.err = nil
	s.output = ""
	s.startedAt = time.Now()
	s.steps = append(s.steps[:0], steps...)
	s.groups = buildSagaGroups(s.steps)
	s.expanded = map[int]bool{}
	s.userToggled = map[int]bool{}
	s.seenStatus = map[int]string{}
	s.cursor = 0
	s.cursorManual = false
	s.visibleLines = nil
}

// Push records an incoming event.
func (s *sagaOverlay) Push(e runtime.Event) {
	if !s.active {
		s.active = true
		s.name = e.Saga
	}
	if e.Done {
		// Stamp the first time we observe done=true so the grace
		// window is anchored to the first Done event (the runtime
		// publishes Done both to the bus and the saga channel, so
		// handleSagaEvent can be called twice — we don't want the
		// second call to reset the window).
		if s.doneAt.IsZero() {
			s.doneAt = time.Now()
		}
		s.done = true
		s.err = e.Err
		s.output = e.Output
		return
	}
	// Update the slot at e.Index (grow slice as needed).
	for len(s.events) <= e.Index {
		s.events = append(s.events, runtime.Event{})
	}
	s.events[e.Index] = e

	// If the overlay was Start()ed without a step skeleton (legacy
	// callers) but the runtime now carries a Category, lazily build a
	// one-step group for that event so hierarchical rendering kicks in.
	// We still render flat when nothing carries a Category.
	if len(s.steps) <= e.Index {
		for len(s.steps) <= e.Index {
			s.steps = append(s.steps, registry.Step{})
		}
		s.steps[e.Index] = registry.Step{Label: e.Step, Category: e.Category}
		s.groups = buildSagaGroups(s.steps)
	}
}

// minDoneDisplay is how long the overlay stays visible after a saga
// completes, even if the user hammers esc/enter. Gives a beat for
// reading the outcome (success summary, failure detail, endpoints)
// before anything is torn down.
const minDoneDisplay = 3 * time.Second

// dismissable reports whether the overlay is eligible for an
// enter/esc-driven dismiss right now. Always true while the saga is
// running (esc aborts) and for flat single-phase sagas (nothing to
// read, no point forcing a delay). For hierarchical sagas — the ones
// worth reading — we hold off dismissal until the minDoneDisplay
// grace window elapses so users aren't yanked out by their "confirm"
// muscle memory.
//
// On failure we drop the grace window: the user needs to dismiss
// manually anyway (DismissAfter returns nil for errors), so making
// them wait before enter/esc works is just friction.
func (s *sagaOverlay) dismissable() bool {
	if !s.done {
		return true
	}
	if s.err != nil {
		return true
	}
	if !s.hasCategories() {
		return true
	}
	if s.doneAt.IsZero() {
		return true
	}
	return time.Since(s.doneAt) >= minDoneDisplay
}

// elapsed returns the saga's wall-clock duration, truncated to whole
// seconds. While the saga is running it advances with the wall clock;
// once the first done event is observed it freezes at doneAt-startedAt
// so the elapsed counter doesn't keep ticking past completion (and
// past the visible failure surface).
func (s *sagaOverlay) elapsed() time.Duration {
	if s.done && !s.doneAt.IsZero() {
		return s.doneAt.Sub(s.startedAt).Truncate(time.Second)
	}
	return time.Since(s.startedAt).Truncate(time.Second)
}

// DismissAfter returns a command that sends dismissSagaMsg after a delay,
// giving the user time to read the completed workflow steps. The
// success-case delay matches minDoneDisplay so the auto-dismiss and
// manual-dismiss grace windows agree.
//
// On failure we return nil — the overlay stays put until the user
// hits enter or esc. Auto-dismissing an error overlay races the user
// reading the failure detail, which is the one thing they actually
// need to see. dismissable() still returns true immediately on
// failure so enter/esc work right away.
func (s *sagaOverlay) DismissAfter() tea.Cmd {
	if s.err != nil {
		return nil
	}
	return tea.Tick(minDoneDisplay, func(_ time.Time) tea.Msg { return dismissSagaMsg{} })
}

// HandleKey consumes a keystroke while the overlay is active. Returns
// true when the key was handled (the caller should NOT fall through to
// the dismiss-on-enter / dismiss-on-esc path).
//
// Nav keys (j/k/enter/space/→/←/e/c) work only when the overlay has at
// least one real category group. For flat sagas we keep today's
// "any-key-consumed" behavior so Enter dismisses the completed overlay.
func (s *sagaOverlay) HandleKey(key string) bool {
	if !s.hasCategories() {
		return false
	}
	switch key {
	case "j", "down":
		if s.cursor < len(s.visibleLines)-1 {
			s.cursor++
			s.cursorManual = true
		}
		return true
	case "k", "up":
		if s.cursor > 0 {
			s.cursor--
			s.cursorManual = true
		}
		return true
	case "right", "l":
		if gid, ok := s.cursorHeader(); ok {
			s.expanded[gid] = true
			s.userToggled[gid] = true
			return true
		}
		return false
	case "left", "h":
		if gid, ok := s.cursorHeader(); ok {
			s.expanded[gid] = false
			s.userToggled[gid] = true
			return true
		}
		return false
	case "enter", " ", "space":
		// Toggle the group under (or containing) the cursor. Works
		// on category headers AND on their indented step rows so
		// users don't have to precisely land on the header line —
		// previously enter on a step row fell through to the
		// dismiss handler, which made the toggle feel flaky
		// ("sometimes it works, sometimes it doesn't"). Flat
		// (uncategorized) rows have no group to toggle, so we let
		// enter fall through to dismiss-on-done as before.
		if gid, ok := s.cursorGroup(); ok {
			g := s.groups[gid]
			s.expanded[g.start] = !s.expanded[g.start]
			s.userToggled[g.start] = true
			return true
		}
		// Enter on a flat row or outside any group: let the outer
		// dismiss-on-done path handle it.
		return false
	case "e":
		for i := range s.groups {
			if s.groups[i].flat {
				continue
			}
			s.expanded[s.groups[i].start] = true
			s.userToggled[s.groups[i].start] = true
		}
		return true
	case "c":
		for i := range s.groups {
			if s.groups[i].flat {
				continue
			}
			s.expanded[s.groups[i].start] = false
			s.userToggled[s.groups[i].start] = true
		}
		return true
	}
	return false
}

// cursorHeader returns the group index for the header line under the
// cursor, or (-1, false) if the cursor isn't on a category header.
// Used by the arrow keys so → only expands and ← only collapses when
// the user is precisely on the header, keeping the gesture unambiguous.
func (s *sagaOverlay) cursorHeader() (int, bool) {
	if s.cursor < 0 || s.cursor >= len(s.visibleLines) {
		return -1, false
	}
	ln := s.visibleLines[s.cursor]
	if ln.kind != sagaLineHeader {
		return -1, false
	}
	return ln.groupID, true
}

// cursorGroup returns the group index for the non-flat group under
// the cursor, whether the cursor sits on the header or on one of the
// group's step rows. Used by enter/space so the toggle works from
// anywhere inside a category — the most common source of "the toggle
// doesn't work" reports. Flat step rows return (-1, false) since
// they have no group to toggle.
func (s *sagaOverlay) cursorGroup() (int, bool) {
	if s.cursor < 0 || s.cursor >= len(s.visibleLines) {
		return -1, false
	}
	ln := s.visibleLines[s.cursor]
	switch ln.kind {
	case sagaLineHeader, sagaLineStep:
		if ln.groupID >= 0 && ln.groupID < len(s.groups) && !s.groups[ln.groupID].flat {
			return ln.groupID, true
		}
	}
	return -1, false
}

// hasCategories reports whether any group is a real category (non-flat).
// Flat-only sagas take the legacy render path.
func (s *sagaOverlay) hasCategories() bool {
	for _, g := range s.groups {
		if !g.flat {
			return true
		}
	}
	return false
}

// Box renders the overlay. stepSpinner is the current frame of the
// per-step spinner — shown next to Running steps. categorySpinner is
// the current frame of the category-header spinner, intentionally a
// different glyph set so the two levels don't visually blur together
// (see app.spinCategory).
func (s *sagaOverlay) Box(w, h int, stepSpinner, categorySpinner string) string {
	if !s.hasCategories() {
		// Flat sagas have no category headers, so only the step
		// spinner is meaningful here.
		return s.renderFlat(w, stepSpinner)
	}
	return s.renderHierarchical(w, h, stepSpinner, categorySpinner)
}

// Pill renders the minimized status badge plus a muted hint line
// pointing the user back to `-` for restoring the full overlay.
// Color encodes state:
//   - running:   green background, name + current step + elapsed
//   - succeeded: green background, "DONE" + elapsed
//   - failed:    red background, "ERROR" + elapsed
//
// The hint is right-aligned to the pill's width so the two-line
// block reads as a tidy unit anchored to the top-right corner.
func (s *sagaOverlay) Pill() string {
	elapsed := s.elapsed()
	var label, body, pill string
	switch {
	case s.done && s.err != nil:
		label = "ERROR"
		body = fmt.Sprintf("%s  %s  %s", label, s.name, formatSagaDuration(elapsed))
		pill = theme.PillErr.Render(body)
	case s.done:
		label = "DONE"
		body = fmt.Sprintf("%s  %s  %s", label, s.name, formatSagaDuration(elapsed))
		pill = theme.PillOK.Render(body)
	default:
		step := s.currentStepLabel()
		if step == "" {
			body = fmt.Sprintf("%s  %s", s.name, formatSagaDuration(elapsed))
		} else {
			body = fmt.Sprintf("%s  %s  %s", s.name, step, formatSagaDuration(elapsed))
		}
		pill = theme.PillRun.Render(body)
	}
	hint := theme.MutedText.Render("press - to restore")
	// Right-align the hint to the pill's rendered width so the block
	// reads as a unit. Pad with spaces (no styled background) so the
	// padding stays transparent over the underlying content.
	pillW := lipgloss.Width(pill)
	hintW := lipgloss.Width(hint)
	if hintW < pillW {
		hint = strings.Repeat(" ", pillW-hintW) + hint
	}
	return pill + "\n" + hint
}

// currentStepLabel returns the label of the most recent Running
// step, falling back to the latest non-skipped event's label, then
// the empty string. Used by the minimized pill so the user can see
// where the saga is at without restoring the full overlay.
func (s *sagaOverlay) currentStepLabel() string {
	// Scan in reverse: the latest Running event wins.
	for i := len(s.events) - 1; i >= 0; i-- {
		e := s.events[i]
		if e.Step == "" {
			continue
		}
		if e.Status == runtime.Running {
			return e.Step
		}
	}
	// No running step — show the most recent non-skipped step (e.g.
	// during the gap between two steps in a single category).
	for i := len(s.events) - 1; i >= 0; i-- {
		e := s.events[i]
		if e.Step == "" || e.Status == runtime.Skipped {
			continue
		}
		return e.Step
	}
	return ""
}

// renderFlat is the legacy bare-list rendering used by operations with no
// Category metadata. Kept byte-compatible with the pre-categories behavior.
func (s *sagaOverlay) renderFlat(w int, spinnerFrame string) string {
	elapsed := s.elapsed()
	done, total := s.overallCounts()

	// Compute the final outer box width first, then derive the interior
	// width the bar has to fit into. theme.Border.Width sets the outer
	// block width (border + padding included), so if we sized the bar
	// to the outer width it would overflow by boxChrome columns and
	// wrap to a second line inside the modal.
	width := 60
	if w-boxChrome < width {
		width = w - boxChrome
	}
	if width < 24 {
		width = 24
	}
	interior := width - boxChrome
	if interior < 10 {
		interior = 10
	}
	s.bar.SetWidth(interior)

	header := theme.Heading.Render("Running: "+s.name) + "  " + theme.MutedText.Render(elapsed.String())
	if s.done {
		header = theme.Heading.Render(s.name) + "  " + theme.MutedText.Render(elapsed.String())
	}
	// For flat sagas only show the counter / bar when there's more
	// than one meaningful step — a single-step op (instance create,
	// etc.) would just go 0 → 1 with nothing to see in between.
	if total > 1 {
		header += "  " + theme.MutedText.Render(fmt.Sprintf("%d / %d", done, total))
	}
	lines := header + "\n"
	if !s.done && total > 1 {
		pct := float64(done) / float64(total)
		lines += s.bar.ViewAs(pct) + "\n"
	}
	lines += "\n"
	for _, e := range s.events {
		if e.Step == "" {
			continue
		}
		mark := theme.PendMark
		switch e.Status {
		case runtime.Running:
			mark = spinnerFrame
		case runtime.OK:
			mark = theme.OKMark
		case runtime.Failed:
			mark = theme.ErrMark
		case runtime.Skipped:
			continue
		}
		lines += fmt.Sprintf("  %s %s\n", mark, e.Step)
	}
	if s.done {
		lines += "\n"
		if s.err != nil {
			lines += theme.Err.Render("✗ failed: "+s.err.Error()) + "\n"
		} else {
			lines += theme.OK.Render("✓ complete") + "\n"
			if s.output != "" {
				lines += "\n" + s.output + "\n"
			}
		}
		lines += "\n" + theme.MutedText.Render("- minimize · esc/enter to close")
	}
	return theme.Border.Width(width).Render(lines)
}

// renderHierarchical draws category headers with expansion chevrons and
// indented step rows under expanded groups. Called when at least one
// group is a real category (non-flat).
//
// Two spinner frames are threaded through: categorySpinner drives the
// header glyph for Running categories, stepSpinner drives the per-step
// glyph for Running steps. Keeping them distinct avoids the visual
// noise of two identical rotating spinners stacked on top of each other.
func (s *sagaOverlay) renderHierarchical(w, h int, stepSpinner, categorySpinner string) string {
	// Resolve derived status + expanded-ness for every group. Must run
	// before we build visibleLines so the chevron and counts match.
	s.refreshGroupStates()

	var lines []string
	var visible []sagaLine

	// Compute the interior (content) width up front so the progress
	// bar can be sized to exactly fit inside the box chrome. Without
	// this, SetWidth would use a stale "whole terminal" value and the
	// bar would overflow theme.Border's border+padding and wrap onto
	// a second line. The final outer width will be interior+boxChrome;
	// we may grow interior later to fit a wider line (capped at maxW).
	maxInterior := w - boxChrome
	if maxInterior < 36 {
		maxInterior = 36
	}
	if maxInterior > 100-boxChrome {
		maxInterior = 100 - boxChrome
	}
	interior := 60 - boxChrome // default content width matching legacy 60 outer
	if interior < 24 {
		interior = 24
	}
	if interior > maxInterior {
		interior = maxInterior
	}
	s.bar.SetWidth(interior)

	elapsed := s.elapsed()
	done, total := s.overallCounts()
	header := theme.Heading.Render("Running: "+s.name) + "  " + theme.MutedText.Render(elapsed.String())
	if s.done {
		header = theme.Heading.Render(s.name) + "  " + theme.MutedText.Render(elapsed.String())
	}
	// Overall "N / M" counter: appended to the title line so the
	// top-line status is always scannable. Only shown when we know
	// the total (i.e. after Start() or the first non-zero Total
	// field has arrived via Push); suppresses 0/0 noise on the first
	// pre-event render.
	if total > 0 {
		header += "  " + theme.MutedText.Render(fmt.Sprintf("%d / %d", done, total))
	}
	lines = append(lines, header)

	// Progress bar under the header. Hidden once the saga is done so
	// the completion summary isn't crowded, mirroring the CLI live
	// renderer's behavior. While running, a full-width blended bar
	// ticks as steps transition to OK / Failed / Skipped.
	if !s.done && total > 0 {
		pct := float64(done) / float64(total)
		lines = append(lines, s.bar.ViewAs(pct))
	}
	lines = append(lines, "")

	// Seed the cursor to the first running category the first time we
	// have one, unless the user has already taken manual control.
	if !s.cursorManual {
		s.cursor = s.firstRunningOrZero()
	}

	for gi := range s.groups {
		g := s.groups[gi]
		if g.flat {
			// Flat (uncategorized) step: render flush-left.
			lineIdx := len(visible)
			visible = append(visible, sagaLine{kind: sagaLineFlatStep, groupID: gi, stepIdx: g.start})
			lines = append(lines, s.renderFlatStepLine(g.start, stepSpinner, lineIdx == s.cursor))
			continue
		}
		st := s.computedStatus(g)
		expanded := s.expanded[g.start]
		// Header line — uses categorySpinner (distinct from step spinner).
		hdrIdx := len(visible)
		visible = append(visible, sagaLine{kind: sagaLineHeader, groupID: gi, stepIdx: -1})
		lines = append(lines, s.renderGroupHeader(g, st, expanded, categorySpinner, hdrIdx == s.cursor))
		// Step rows (when expanded).
		if expanded {
			for i := g.start; i <= g.end; i++ {
				if i >= len(s.events) {
					// Unstarted: still render so users see the plan.
					stepIdx := len(visible)
					visible = append(visible, sagaLine{kind: sagaLineStep, groupID: gi, stepIdx: i})
					lines = append(lines, s.renderPendingStepLine(i, stepIdx == s.cursor))
					continue
				}
				ev := s.events[i]
				if ev.Status == runtime.Skipped {
					continue
				}
				stepIdx := len(visible)
				visible = append(visible, sagaLine{kind: sagaLineStep, groupID: gi, stepIdx: i})
				lines = append(lines, s.renderStepLine(ev, stepSpinner, stepIdx == s.cursor))
			}
		}
	}

	// Clamp cursor to visible range (auto-collapse may shrink it).
	if s.cursor >= len(visible) {
		s.cursor = len(visible) - 1
	}
	if s.cursor < 0 {
		s.cursor = 0
	}
	s.visibleLines = visible

	if s.done {
		lines = append(lines, "")
		if s.err != nil {
			lines = append(lines, theme.Err.Render("✗ failed: "+s.err.Error()))
		} else {
			lines = append(lines, theme.OK.Render("✓ complete"))
			if s.output != "" {
				lines = append(lines, "", s.output)
			}
		}
	}

	// Footer hint. Muted and small; replaces the legacy dismiss line.
	footer := theme.MutedText.Render("j/k navigate · enter toggle · e expand all · c collapse all · - minimize · esc close")
	lines = append(lines, "", footer)

	body := strings.Join(lines, "\n")

	// Grow interior (content width) to fit the widest line, capped at
	// the terminal-derived maxInterior. The bar was already sized to
	// the initial interior; if interior grows past that here we leave
	// the bar at its smaller size rather than re-rendering — it still
	// fits strictly inside the chrome, which is all we need to avoid
	// the overflow-wrap bug.
	for _, ln := range lines {
		if wv := lipgloss.Width(ln); wv > interior {
			interior = wv
		}
	}
	if interior > maxInterior {
		interior = maxInterior
	}
	outer := interior + boxChrome

	// Height cap: let the modal grow, but add a "… N more" footer if we
	// have to clip. Full scrolling is a v2 concern.
	maxH := h - 4
	if maxH > 0 {
		rendered := strings.Split(body, "\n")
		if len(rendered) > maxH {
			keep := maxH - 1
			if keep < 1 {
				keep = 1
			}
			more := len(rendered) - keep
			rendered = append(rendered[:keep],
				theme.MutedText.Render(fmt.Sprintf("… %d more lines", more)))
			body = strings.Join(rendered, "\n")
		}
	}

	return theme.Border.Width(outer).Render(body)
}

// renderGroupHeader returns the "▶/▼ <mark> <name>  (n/total)   elapsed/took Ns"
// line for a category. The chevron color / counts / suffix all derive from
// the group's current computed status.
func (s *sagaOverlay) renderGroupHeader(g sagaGroup, st string, expanded bool, spinnerFrame string, cursor bool) string {
	chev := "▶"
	if expanded {
		chev = "▼"
	}
	mark := statusMark(st, spinnerFrame)
	// Truncate long names so counts + elapsed tail always fit.
	name := g.name
	maxName := 60
	if len(name) > maxName {
		name = name[:maxName-1] + "…"
	}
	done, total, allSkipped := s.groupCounts(g)
	counts := fmt.Sprintf("(%d/%d)", done, total)
	suffix := s.groupSuffix(g, st)
	if allSkipped {
		counts = "(all skipped)"
	}
	line := fmt.Sprintf("%s %s %s  %s", chev, mark, name, theme.MutedText.Render(counts))
	if suffix != "" {
		line += "  " + theme.MutedText.Render(suffix)
	}
	if cursor {
		line = theme.Value.Render("▸ ") + line
	} else {
		line = "  " + line
	}
	return line
}

// renderStepLine is one indented step row under an expanded group.
func (s *sagaOverlay) renderStepLine(ev runtime.Event, spinnerFrame string, cursor bool) string {
	mark := stepMark(ev.Status, spinnerFrame)
	label := ev.Step
	if ev.Status == runtime.Failed && ev.Err != nil {
		label += " — " + theme.Err.Render(ev.Err.Error())
	}
	indent := "      "
	if cursor {
		indent = "    " + theme.Value.Render("▸ ")
	}
	return indent + mark + " " + label
}

// renderPendingStepLine is the indented placeholder for a not-yet-started
// step inside an expanded group.
func (s *sagaOverlay) renderPendingStepLine(stepIdx int, cursor bool) string {
	label := ""
	if stepIdx < len(s.steps) {
		label = s.steps[stepIdx].Label
	}
	if label == "" {
		label = "(pending)"
	}
	indent := "      "
	if cursor {
		indent = "    " + theme.Value.Render("▸ ")
	}
	return indent + theme.PendMark + " " + theme.MutedText.Render(label)
}

// renderFlatStepLine renders one uncategorized step at top level. Same
// indentation as the legacy overlay so mixed sagas visually match.
func (s *sagaOverlay) renderFlatStepLine(stepIdx int, spinnerFrame string, cursor bool) string {
	if stepIdx >= len(s.events) || s.events[stepIdx].Step == "" {
		label := ""
		if stepIdx < len(s.steps) {
			label = s.steps[stepIdx].Label
		}
		if label == "" {
			label = "(pending)"
		}
		if cursor {
			return theme.Value.Render("▸ ") + theme.PendMark + " " + theme.MutedText.Render(label)
		}
		return "  " + theme.PendMark + " " + theme.MutedText.Render(label)
	}
	ev := s.events[stepIdx]
	mark := stepMark(ev.Status, spinnerFrame)
	label := ev.Step
	if ev.Status == runtime.Failed && ev.Err != nil {
		label += " — " + theme.Err.Render(ev.Err.Error())
	}
	if cursor {
		return theme.Value.Render("▸ ") + mark + " " + label
	}
	return "  " + mark + " " + label
}

// overallCounts aggregates progress across every step of the saga,
// independent of category grouping. Drives the header's "N / M"
// counter and the progress bar fill percentage. Skipped steps are
// excluded from both sides so the visible denominator matches what
// users actually see scroll by; OK and Failed both count as
// "completed" (no sense rewinding the bar on a failure).
func (s *sagaOverlay) overallCounts() (done, total int) {
	size := len(s.steps)
	if size == 0 {
		return 0, 0
	}
	skipped := 0
	for i := 0; i < size; i++ {
		if i >= len(s.events) {
			continue
		}
		switch s.events[i].Status {
		case runtime.OK, runtime.Failed:
			done++
		case runtime.Skipped:
			skipped++
		}
	}
	total = size - skipped
	if total < 0 {
		total = 0
	}
	return done, total
}

// groupCounts returns completed, total, and allSkipped for a category.
// Skipped steps are excluded from both sides so counts reflect real work.
// allSkipped is true when every member step was skipped (rendered with a
// muted "(all skipped)" suffix).
func (s *sagaOverlay) groupCounts(g sagaGroup) (done, total int, allSkipped bool) {
	skipped := 0
	size := g.end - g.start + 1
	for i := g.start; i <= g.end; i++ {
		if i >= len(s.events) {
			continue
		}
		ev := s.events[i]
		switch ev.Status {
		case runtime.OK, runtime.Failed:
			done++
		case runtime.Skipped:
			skipped++
		}
	}
	total = size - skipped
	if total < 0 {
		total = 0
	}
	allSkipped = size > 0 && skipped == size
	return done, total, allSkipped
}

// groupSuffix builds the "elapsed/took Ns" tail for a category header.
func (s *sagaOverlay) groupSuffix(g sagaGroup, st string) string {
	start, end, any := s.groupTimeWindow(g)
	if !any {
		return ""
	}
	switch st {
	case "running", "needs_input":
		d := time.Since(start).Truncate(time.Second)
		return "elapsed " + formatSagaDuration(d)
	case "ok", "failed":
		if end.IsZero() || end.Before(start) {
			return ""
		}
		d := end.Sub(start).Truncate(time.Second)
		return "took " + formatSagaDuration(d)
	}
	return ""
}

// groupTimeWindow returns (firstEventAt, lastEventAt, any) for events
// recorded against a category. When no member step has emitted any
// event yet, any is false.
func (s *sagaOverlay) groupTimeWindow(g sagaGroup) (time.Time, time.Time, bool) {
	var start, end time.Time
	any := false
	for i := g.start; i <= g.end; i++ {
		if i >= len(s.events) {
			continue
		}
		ev := s.events[i]
		if ev.Step == "" || ev.At.IsZero() {
			continue
		}
		if !any || ev.At.Before(start) {
			start = ev.At
		}
		if !any || ev.At.After(end) {
			end = ev.At
		}
		any = true
	}
	return start, end, any
}

// computedStatus returns one of pending / running / ok / failed / needs_input
// for a category.
func (s *sagaOverlay) computedStatus(g sagaGroup) string {
	var (
		anyRunning, anyFailed, anyNeeds bool
		okCount, skipped                int
		unstarted                       int
	)
	size := g.end - g.start + 1
	for i := g.start; i <= g.end; i++ {
		if i >= len(s.events) || s.events[i].Step == "" {
			unstarted++
			continue
		}
		switch s.events[i].Status {
		case runtime.Running:
			anyRunning = true
		case runtime.Failed:
			anyFailed = true
		case runtime.NeedsInput:
			anyNeeds = true
		case runtime.OK:
			okCount++
		case runtime.Skipped:
			skipped++
		}
	}
	switch {
	case anyFailed:
		return "failed"
	case anyNeeds:
		return "needs_input"
	case anyRunning || (unstarted > 0 && okCount+skipped > 0):
		return "running"
	case okCount+skipped == size:
		return "ok"
	default:
		return "pending"
	}
}

// refreshGroupStates applies auto-expansion rules for every group based
// on its current computed status. Auto rules fire once per transition
// (tracked via seenStatus). Manual toggles recorded in userToggled
// prevent any future auto change.
func (s *sagaOverlay) refreshGroupStates() {
	for i := range s.groups {
		g := s.groups[i]
		if g.flat {
			continue
		}
		st := s.computedStatus(g)
		last := s.seenStatus[g.start]
		if st == last {
			continue
		}
		s.seenStatus[g.start] = st
		if s.userToggled[g.start] {
			continue
		}
		switch st {
		case "running", "failed", "needs_input":
			s.expanded[g.start] = true
		case "ok":
			s.expanded[g.start] = false
		}
	}
}

// firstRunningOrZero returns the visible-line index of the first running
// category header, or 0 when nothing is running yet. Used to seed the
// cursor before the user takes control.
func (s *sagaOverlay) firstRunningOrZero() int {
	lineIdx := 0
	for gi := range s.groups {
		g := s.groups[gi]
		if g.flat {
			lineIdx++
			continue
		}
		if s.computedStatus(g) == "running" {
			return lineIdx
		}
		lineIdx++
		if s.expanded[g.start] {
			for i := g.start; i <= g.end; i++ {
				if i < len(s.events) && s.events[i].Status == runtime.Skipped {
					continue
				}
				lineIdx++
			}
		}
	}
	return 0
}

// statusMark returns the glyph shown next to a category header for a
// derived status string.
func statusMark(st, spinnerFrame string) string {
	switch st {
	case "running":
		return spinnerFrame
	case "ok":
		return theme.OKMark
	case "failed":
		return theme.ErrMark
	case "needs_input":
		return theme.Warn.Render("?")
	}
	return theme.PendMark
}

// stepMark is the per-step glyph for a runtime.Status.
func stepMark(status runtime.Status, spinnerFrame string) string {
	switch status {
	case runtime.Running:
		return spinnerFrame
	case runtime.OK:
		return theme.OKMark
	case runtime.Failed:
		return theme.ErrMark
	case runtime.NeedsInput:
		return theme.Warn.Render("?")
	}
	return theme.PendMark
}

// buildSagaGroups walks the step slice splitting it into consecutive-run
// groups of same-Category steps and flat single-step entries for any
// Category == "" slot.
func buildSagaGroups(steps []registry.Step) []sagaGroup {
	var out []sagaGroup
	i := 0
	for i < len(steps) {
		cat := steps[i].Category
		if cat == "" {
			out = append(out, sagaGroup{flat: true, start: i, end: i})
			i++
			continue
		}
		// Scan forward while the next step has the same category.
		j := i
		for j+1 < len(steps) && steps[j+1].Category == cat {
			j++
		}
		out = append(out, sagaGroup{name: cat, start: i, end: j})
		i = j + 1
	}
	return out
}

// formatSagaDuration renders a Duration as "<1s" for sub-second values and
// as the stdlib default for the rest. Matches the plan's preference for
// "<1s" over "0s".
func formatSagaDuration(d time.Duration) string {
	if d <= 0 || d < time.Second {
		return "<1s"
	}
	return d.String()
}
