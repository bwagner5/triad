package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
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
	w, h      int

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

func newSagaOverlay() sagaOverlay {
	return sagaOverlay{
		expanded:    map[int]bool{},
		userToggled: map[int]bool{},
		seenStatus:  map[int]string{},
	}
}

func (s *sagaOverlay) SetSize(w, h int) { s.w, s.h = w, h }
func (s *sagaOverlay) Active() bool     { return s.active }
func (s *sagaOverlay) Clear() {
	// Preserve size only; drop every other field.
	*s = sagaOverlay{
		w: s.w, h: s.h,
		expanded:    map[int]bool{},
		userToggled: map[int]bool{},
		seenStatus:  map[int]string{},
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

// DismissAfter returns a command that sends dismissSagaMsg after a delay,
// giving the user time to read the completed workflow steps.
func (s *sagaOverlay) DismissAfter() tea.Cmd {
	d := 4 * time.Second
	if s.err != nil {
		d = 6 * time.Second
	}
	return tea.Tick(d, func(_ time.Time) tea.Msg { return dismissSagaMsg{} })
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
		if gid, ok := s.cursorHeader(); ok {
			s.expanded[gid] = !s.expanded[gid]
			s.userToggled[gid] = true
			return true
		}
		// Enter on a non-header line: let the outer dismiss-on-done
		// path handle it. Returning false ensures the completed saga
		// can still be dismissed with Enter on a step/flat row.
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

// Box renders the overlay. spinnerFrame is the current frame of the app's
// spinner — passed in so Running steps show live animation.
func (s *sagaOverlay) Box(w, h int, spinnerFrame string) string {
	if !s.hasCategories() {
		return s.renderFlat(w, spinnerFrame)
	}
	return s.renderHierarchical(w, h, spinnerFrame)
}

// renderFlat is the legacy bare-list rendering used by operations with no
// Category metadata. Kept byte-compatible with the pre-categories behavior.
func (s *sagaOverlay) renderFlat(w int, spinnerFrame string) string {
	elapsed := time.Since(s.startedAt).Truncate(time.Second)
	header := theme.Heading.Render("Running: "+s.name) + "  " + theme.MutedText.Render(elapsed.String())
	if s.done {
		header = theme.Heading.Render(s.name) + "  " + theme.MutedText.Render(elapsed.String())
	}
	lines := header + "\n\n"
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
		lines += "\n" + theme.MutedText.Render("press esc or enter to close")
	}
	width := 60
	if w < width+4 {
		width = w - 4
	}
	return theme.Border.Width(width).Render(lines)
}

// renderHierarchical draws category headers with expansion chevrons and
// indented step rows under expanded groups. Called when at least one
// group is a real category (non-flat).
func (s *sagaOverlay) renderHierarchical(w, h int, spinnerFrame string) string {
	// Resolve derived status + expanded-ness for every group. Must run
	// before we build visibleLines so the chevron and counts match.
	s.refreshGroupStates()

	var lines []string
	var visible []sagaLine

	elapsed := time.Since(s.startedAt).Truncate(time.Second)
	header := theme.Heading.Render("Running: "+s.name) + "  " + theme.MutedText.Render(elapsed.String())
	if s.done {
		header = theme.Heading.Render(s.name) + "  " + theme.MutedText.Render(elapsed.String())
	}
	lines = append(lines, header, "")

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
			lines = append(lines, s.renderFlatStepLine(g.start, spinnerFrame, lineIdx == s.cursor))
			continue
		}
		st := s.computedStatus(g)
		expanded := s.expanded[g.start]
		// Header line.
		hdrIdx := len(visible)
		visible = append(visible, sagaLine{kind: sagaLineHeader, groupID: gi, stepIdx: -1})
		lines = append(lines, s.renderGroupHeader(g, st, expanded, spinnerFrame, hdrIdx == s.cursor))
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
				lines = append(lines, s.renderStepLine(ev, spinnerFrame, stepIdx == s.cursor))
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
	footer := theme.MutedText.Render("j/k navigate · enter toggle · e expand all · c collapse all · esc close")
	lines = append(lines, "", footer)

	body := strings.Join(lines, "\n")

	// Compute width to fit the widest line, capped at terminal-width-4
	// or 100, whichever is smaller.
	width := 60
	for _, ln := range lines {
		if wv := lipgloss.Width(ln); wv > width {
			width = wv
		}
	}
	maxW := w - 4
	if maxW < 40 {
		maxW = 40
	}
	if width > maxW {
		width = maxW
	}
	if width > 100 {
		width = 100
	}

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

	return theme.Border.Width(width).Render(body)
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
