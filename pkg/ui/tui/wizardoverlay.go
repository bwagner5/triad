package tui

import (
	"context"
	"fmt"
	"os"
	"strings"

	"charm.land/bubbles/v2/filepicker"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/bwagner5/triad/pkg/registry"
	"github.com/bwagner5/triad/pkg/trace"
	"github.com/bwagner5/triad/pkg/ui/theme"
	"github.com/bwagner5/triad/pkg/ui/wizardstate"
)

// wizardOverlay is a form overlay that shows all fields at once.
// The focused field is editable; tab/shift-tab moves between fields.
//
// Per-field state (answers, choice cursors, multi-select checkboxes,
// loaded Suggest choices, validation errors) lives in wo.state — a
// wizardstate.State that the inline CLI wizard also uses, so the two
// UIs share semantics for visibility cascades, GoBack, and submit.
type wizardOverlay struct {
	active   bool
	ctx      context.Context
	resource *registry.Resource
	op       *registry.Operation
	fields   []registry.Field
	input    registry.Input
	state    *wizardstate.State

	// Widget instances. State holds the answer slots; widgets render
	// the focused entry. inputs[i].Value() is mirrored into State on
	// every keystroke so When predicates see in-flight text.
	inputs  []textinput.Model
	pickers []*filepicker.Model
	// touched tracks whether the user has typed in each text field.
	// Once touched, the widget value is authoritative over the state's
	// Default/Prefill seed even when empty.
	touched []bool
	// handoff is true when the overlay was opened from a Ctrl+T switch
	// with pre-existing state. Defaults are shown as placeholders
	// instead of pre-filled values so the user can confirm them.
	handoff bool

	spin spinner.Model
	// busy is true after submit() while we wait for the saga or next
	// wizard to advance. Renders a spinner+message instead of dismissing
	// the overlay, so users get continuous feedback.
	busy           bool
	preamble       string // optional text block above the fields
	preambleScroll int    // scroll offset for long preambles
	w, h           int
}

func newWizardOverlay() wizardOverlay {
	return wizardOverlay{spin: spinner.New()}
}

func (wo *wizardOverlay) Active() bool     { return wo.active }
func (wo *wizardOverlay) SetSize(w, h int) { wo.w, wo.h = w, h }

func (wo *wizardOverlay) Show(ctx context.Context, res *registry.Resource, op *registry.Operation, fields []registry.Field, input registry.Input) tea.Cmd {
	return wo.ShowWithPreamble(ctx, res, op, fields, input, "")
}

func (wo *wizardOverlay) ShowWithPreamble(ctx context.Context, res *registry.Resource, op *registry.Operation, fields []registry.Field, input registry.Input, preamble string) tea.Cmd {
	return wo.ShowWithState(ctx, res, op, fields, input, nil, preamble)
}

// ShowWithState is like ShowWithPreamble but accepts a pre-built
// wizardstate.State to resume from (e.g. after a Ctrl+T handoff from
// the inline CLI wizard). When state is nil, a fresh state is built
// from fields and input.
func (wo *wizardOverlay) ShowWithState(ctx context.Context, res *registry.Resource, op *registry.Operation, fields []registry.Field, input registry.Input, state *wizardstate.State, preamble string) tea.Cmd {
	wo.active = true
	wo.busy = false
	wo.ctx = ctx
	wo.resource = res
	wo.op = op
	wo.fields = fields
	wo.input = input
	wo.handoff = state != nil
	if state != nil {
		wo.state = state
	} else {
		wo.state = wizardstate.New(fields, input)
	}
	wo.preamble = preamble
	wo.preambleScroll = 0

	n := len(fields)
	wo.inputs = make([]textinput.Model, n)
	wo.pickers = make([]*filepicker.Model, n)
	wo.touched = make([]bool, n)

	var cmds []tea.Cmd
	for i, f := range fields {
		if f.File || f.EffectiveKind() == registry.KindFile {
			fp := filepicker.New()
			fp.AllowedTypes = f.AllowedExts
			fp.ShowHidden = false
			fp.AutoHeight = false
			fp.SetHeight(10)
			if cwd, err := os.Getwd(); err == nil {
				fp.CurrentDirectory = cwd
			}
			// Restore a previously selected path from the state (e.g.
			// Ctrl+T handoff from the CLI wizard where the user already
			// picked a file).
			if path := wo.state.Entry(i).FilePath; path != "" {
				fp.Path = path
			}
			wo.pickers[i] = &fp
			cmds = append(cmds, fp.Init())
			continue
		}
		ti := textinput.New()
		ti.Prompt = "› "
		ti.Placeholder = f.Help
		if f.Sensitive {
			ti.EchoMode = textinput.EchoPassword
		}
		// State already seeded entry.Text from Input>Prefill>Default.
		// Suggest fields render via choices, not the textinput, so
		// skip the SetValue call for them.
		if f.Suggest == nil {
			entry := wo.state.Entry(i)
			if entry.Text != "" {
				if wo.handoff && entry.Defaulted && !entry.Committed {
					ti.Placeholder = entry.Text
				} else {
					ti.SetValue(entry.Text)
				}
			}
		}
		wo.inputs[i] = ti

		if f.Suggest != nil {
			// Resume path: state may already carry fetched choices
			// from a prior CLI-side Suggest call. Don't re-fetch.
			if len(wo.state.Entry(i).Choices) > 0 {
				continue
			}
			wo.state.SetLoading(i, true)
			cmds = append(cmds, wo.fetchSuggest(i, f))
		}
	}
	// Resume path (Ctrl+T handoff): focus the field the user was on
	// in the CLI wizard. Fresh path: start at field 0. In both cases,
	// fall back to FirstVisible if the chosen index is hidden.
	target := 0
	if state != nil {
		target = state.Idx()
	}
	cmds = append(cmds, wo.focusField(target), wo.spin.Tick)
	if !wo.state.FieldVisible(target) {
		if j := wo.state.FirstVisible(); j >= 0 {
			cmds = append(cmds, wo.focusField(j))
		}
	}
	return tea.Batch(cmds...)
}

func (wo *wizardOverlay) Clear() {
	wo.active = false
	wo.busy = false
	wo.handoff = false
	wo.fields = nil
	wo.inputs = nil
	wo.pickers = nil
	wo.touched = nil
	wo.state = nil
	wo.preamble = ""
	wo.preambleScroll = 0
}

// idx returns the currently focused field index. Convenience for
// migrated call sites that previously used wo.idx.
func (wo *wizardOverlay) idx() int {
	if wo.state == nil {
		return 0
	}
	return wo.state.Idx()
}

func (wo *wizardOverlay) isSelect(i int) bool {
	return i < len(wo.fields) && wo.fields[i].Suggest != nil
}

// isMulti reports whether field i is a multi-select Suggest field.
func (wo *wizardOverlay) isMulti(i int) bool {
	return wo.isSelect(i) && wo.fields[i].Multi
}

func (wo *wizardOverlay) isFile(i int) bool {
	return i < len(wo.fields) && (wo.fields[i].File || wo.fields[i].EffectiveKind() == registry.KindFile)
}

// fieldVisible reports whether field i should be shown in the overlay.
// Thin pass-through to State.
func (wo *wizardOverlay) fieldVisible(i int) bool {
	if wo.state == nil {
		return false
	}
	return wo.state.FieldVisible(i)
}

func (wo *wizardOverlay) nextVisible(i int) int {
	if wo.state == nil {
		return -1
	}
	return wo.state.NextVisible(i)
}

func (wo *wizardOverlay) prevVisible(i int) int {
	if wo.state == nil {
		return -1
	}
	return wo.state.PrevVisible(i)
}

func (wo *wizardOverlay) firstVisible() int {
	if wo.state == nil {
		return -1
	}
	return wo.state.FirstVisible()
}

func (wo *wizardOverlay) lastVisible() int {
	if wo.state == nil {
		return -1
	}
	return wo.state.LastVisible()
}

// fieldValue returns the value the user has selected/typed for field i.
// Thin pass-through to State; preserved as a method so existing callers
// (and tests) keep their call sites.
func (wo *wizardOverlay) fieldValue(i int) string {
	if wo.state == nil {
		return ""
	}
	// For File fields, the picker's Path is the source of truth — sync
	// it into State before reading so we don't lag the widget.
	if wo.isFile(i) && wo.pickers[i] != nil {
		wo.state.SetFilePath(i, wo.pickers[i].Path)
	}
	// For text fields, the textinput is the source of truth — but only
	// sync when non-empty, already committed, or user-touched, to avoid
	// overwriting Default/Prefill seeds in state with an empty widget.
	if !wo.isSelect(i) && !wo.isFile(i) && i < len(wo.inputs) {
		v := wo.inputs[i].Value()
		if v != "" || wo.state.Entry(i).Committed || wo.touched[i] {
			wo.state.SetText(i, v)
		}
	}
	return wo.state.Value(i)
}

type wizardSuggestMsg struct {
	idx     int
	choices []registry.Choice
	err     error
}

func (wo *wizardOverlay) fetchSuggest(idx int, f registry.Field) tea.Cmd {
	ctx := wo.ctx
	return func() tea.Msg {
		cs, err := f.Suggest(ctx)
		return wizardSuggestMsg{idx: idx, choices: cs, err: err}
	}
}

func (wo *wizardOverlay) focusField(i int) tea.Cmd {
	// Mirror the departing field's widget value into State so When
	// predicates see the latest typed/picked value when we re-evaluate
	// visibility for downstream fields. Skip uncommitted fields with an
	// empty widget to avoid clobbering Default/Prefill seeds in state.
	if wo.state != nil {
		cur := wo.state.Idx()
		if cur >= 0 && cur < len(wo.fields) {
			if !wo.isSelect(cur) && !wo.isFile(cur) && cur < len(wo.inputs) {
				v := wo.inputs[cur].Value()
				if v != "" || wo.state.Entry(cur).Committed || wo.touched[cur] {
					wo.state.SetText(cur, v)
				}
			}
			if wo.isFile(cur) && wo.pickers[cur] != nil {
				wo.state.SetFilePath(cur, wo.pickers[cur].Path)
			}
		}
		wo.state.Focus(i)
	}
	var cmds []tea.Cmd
	for j := range wo.inputs {
		if j == i && !wo.isSelect(j) && !wo.isFile(j) {
			cmds = append(cmds, wo.inputs[j].Focus())
		} else if wo.inputs[j].Prompt != "" {
			wo.inputs[j].Blur()
		}
	}
	return tea.Batch(cmds...)
}

func (wo *wizardOverlay) submit() tea.Cmd {
	// Mirror text/file widgets into State once before validation so any
	// in-flight typing is included. Skip uncommitted fields where the
	// widget is empty — state already holds the Default/Prefill seed and
	// overwriting it would lose the default the user implicitly accepted.
	for i := range wo.fields {
		if wo.isFile(i) && wo.pickers[i] != nil {
			wo.state.SetFilePath(i, wo.pickers[i].Path)
		} else if !wo.isSelect(i) && i < len(wo.inputs) && wo.inputs[i].Prompt != "" {
			v := wo.inputs[i].Value()
			if v != "" || wo.state.Entry(i).Committed || wo.touched[i] {
				wo.state.SetText(i, v)
			}
		}
	}
	if first := wo.state.Submit(); first >= 0 {
		return wo.focusField(first)
	}
	wo.state.ApplyToInput(wo.input)
	wo.busy = true
	return func() tea.Msg {
		return wizardDoneMsg{resource: wo.resource, op: wo.op, input: wo.input}
	}
}

// validateField runs the per-field Validate (if any) against the current
// value. Returns nil if valid or unset-and-optional, an error otherwise.
func (wo *wizardOverlay) validateField(i int) error {
	if i >= len(wo.fields) {
		return nil
	}
	f := wo.fields[i]
	val := wo.fieldValue(i)
	if val == "" {
		if f.Required {
			return errRequired
		}
		return nil
	}
	return f.ValidateValue(val)
}

var errRequired = fmt.Errorf("required")

// wizardDoneMsg is posted when the wizard overlay finishes collecting all fields.
type wizardDoneMsg struct {
	resource *registry.Resource
	op       *registry.Operation
	input    registry.Input
}

// Update processes messages for the wizard overlay. Returns (consumed, cmd).
func (wo *wizardOverlay) Update(msg tea.Msg) (bool, tea.Cmd) {
	if !wo.active {
		return false, nil
	}
	// Busy overlay absorbs key presses (wizard was submitted; nothing
	// to interact with yet) but still forwards spinner ticks so the
	// animation keeps running.
	if wo.busy {
		if _, isKey := msg.(tea.KeyPressMsg); isKey {
			return true, nil
		}
		if _, isTick := msg.(spinner.TickMsg); isTick {
			var cmd tea.Cmd
			wo.spin, cmd = wo.spin.Update(msg)
			return true, cmd
		}
		return true, nil
	}
	// Non-key, non-wizard messages (readDirMsg from filepicker.Init, etc.)
	// need to reach every picker regardless of focus — the picker filters
	// by its own id internally. Without this, pickers built for non-focused
	// fields never receive their initial directory listing and the UI
	// shows "Bummer. No Files Found."
	if _, isKey := msg.(tea.KeyPressMsg); !isKey {
		if _, isSuggest := msg.(wizardSuggestMsg); !isSuggest {
			var cmds []tea.Cmd
			for i := range wo.pickers {
				if wo.pickers[i] == nil {
					continue
				}
				fp := *wo.pickers[i]
				newFp, cmd := fp.Update(msg)
				wo.pickers[i] = &newFp
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
			if len(cmds) > 0 {
				return true, tea.Batch(cmds...)
			}
		}
	}
	cur := wo.idx()
	// If the focused field is a filepicker, let it handle keystrokes.
	// handleKey below still intercepts tab/shift-tab/enter at submit time.
	if wo.isFile(cur) && wo.pickers[cur] != nil {
		if key, ok := msg.(tea.KeyPressMsg); ok {
			if consumed, cmd := wo.fileKey(key); consumed {
				return true, cmd
			}
		}
		fp := *wo.pickers[cur]
		newFp, cmd := fp.Update(msg)
		wo.pickers[cur] = &newFp
		if didSelect, path := newFp.DidSelectFile(msg); didSelect {
			wo.pickers[cur].Path = path
			wo.state.SetFilePath(cur, path)
			// File selected: advance to next visible field if there
			// is one, else submit. Prevents accidental submit when
			// the user expects enter to just commit the file.
			if j := wo.nextVisible(cur); j >= 0 {
				return true, tea.Batch(cmd, wo.focusField(j))
			}
			return true, tea.Batch(cmd, wo.submit())
		}
		return true, cmd
	}
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return true, wo.handleKey(msg)
	case wizardSuggestMsg:
		if msg.idx < len(wo.fields) {
			if msg.err != nil {
				wo.state.SetChoicesErr(msg.idx, msg.err)
			} else {
				wo.state.SetChoices(msg.idx, msg.choices)
			}
		}
		return true, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		wo.spin, cmd = wo.spin.Update(msg)
		return true, cmd
	}
	// Forward to focused text input, then mirror into State so When
	// predicates see in-flight typing.
	if cur < len(wo.inputs) && !wo.isSelect(cur) && !wo.isFile(cur) {
		var cmd tea.Cmd
		wo.inputs[cur], cmd = wo.inputs[cur].Update(msg)
		wo.touched[cur] = true
		wo.state.SetText(cur, wo.inputs[cur].Value())
		return true, cmd
	}
	return true, nil
}

// fileKey handles keys that must act on the overlay even when a filepicker
// is focused (tab/shift-tab/esc, and enter-to-submit once a file has
// been selected). Returns consumed=true when we handled it.
func (wo *wizardOverlay) fileKey(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	cur := wo.idx()
	switch msg.String() {
	case "ctrl+c":
		op, res := wo.op, wo.resource
		wo.Clear()
		return true, func() tea.Msg { return wizardDoneMsg{resource: res, op: op, input: nil} }
	case "tab":
		if j := wo.nextVisible(cur); j >= 0 {
			return true, wo.focusField(j)
		}
		return true, nil
	case "shift+tab":
		if j := wo.prevVisible(cur); j >= 0 {
			return true, wo.focusField(j)
		}
		return true, nil
	case "enter":
		// Enter is overloaded: the filepicker uses it to open a directory
		// OR select a file. Only one of those is "commit the field".
		// If the picker already has a Path committed, advance to next
		// visible field (or submit when on the last visible field).
		if wo.pickers[cur] != nil && wo.pickers[cur].Path != "" {
			if j := wo.nextVisible(cur); j >= 0 {
				return true, wo.focusField(j)
			}
			return true, wo.submit()
		}
		return false, nil
	}
	return false, nil
}

func (wo *wizardOverlay) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	key := msg.String()
	cur := wo.idx()
	e := wo.state.Entry(cur)
	trace.Trace(wo.ctx, "tui wizard key",
		"key", key, "idx", cur, "isSelect", wo.isSelect(cur),
		"loading", e.Loading, "choices", len(e.Choices), "selIdx", e.SelIdx,
	)
	switch key {
	case "ctrl+c", "esc":
		// Preserve the op/resource so the saga's Provide callback can be
		// informed of the cancellation via handleWizardDone(input=nil).
		op, res := wo.op, wo.resource
		wo.Clear()
		return func() tea.Msg { return wizardDoneMsg{resource: res, op: op, input: nil} }
	case "pgup":
		if wo.preamble != "" && wo.preambleScroll > 0 {
			wo.preambleScroll -= 5
			if wo.preambleScroll < 0 {
				wo.preambleScroll = 0
			}
		}
		return nil
	case "pgdown":
		if wo.preamble != "" {
			wo.preambleScroll += 5
		}
		return nil
	case "tab":
		if j := wo.nextVisible(cur); j >= 0 {
			return wo.focusField(j)
		}
		return nil
	case "shift+tab":
		if j := wo.prevVisible(cur); j >= 0 {
			return wo.focusField(j)
		}
		return nil
	case "enter":
		// Enter on a non-last visible field advances to the next
		// visible one (natural typing flow). Enter on the last
		// visible field submits. For select fields, enter on the
		// last visible field also submits; on non-last, it commits
		// the selection and advances.
		if j := wo.nextVisible(cur); j >= 0 {
			// Run field-level validation before advancing so the
			// user doesn't tab past a bad value unknowingly.
			if err := wo.validateField(cur); err != nil {
				wo.state.Entry(cur).Err = err.Error()
				return nil
			}
			wo.state.Entry(cur).Err = ""
			// Mark the field committed so its value renders as
			// confirmed (not a muted default) and state syncs
			// treat the widget as authoritative.
			wo.state.Entry(cur).Committed = true
			return wo.focusField(j)
		}
		return wo.submit()
	}
	// Selection list navigation for Suggest fields.
	// up/down/j/k all move the list cursor. If the current field is a text
	// input, up/down act as tab/shift-tab (field nav).
	if wo.isSelect(cur) && !e.Loading {
		switch key {
		case "j", "down":
			if e.SelIdx < len(e.Choices)-1 {
				wo.state.SetSelIdx(cur, e.SelIdx+1)
			}
			return nil
		case "k", "up":
			if e.SelIdx > 0 {
				wo.state.SetSelIdx(cur, e.SelIdx-1)
			}
			return nil
		case " ", "space":
			// Space toggles selection only for Multi fields.
			// Regular Suggest fields ignore it so typing into a
			// would-be text input isn't mistaken for selection.
			if wo.isMulti(cur) && len(e.Choices) > 0 {
				wo.state.ToggleMulti(cur, e.SelIdx)
			}
			return nil
		case "pgup", "pgdown":
			// fall through to preamble scroll handling below
		default:
			return nil // consume other keys on select fields
		}
	}
	// Text input field: up/down navigate between visible fields.
	switch key {
	case "down":
		if j := wo.nextVisible(cur); j >= 0 {
			return wo.focusField(j)
		}
		return nil
	case "up":
		if j := wo.prevVisible(cur); j >= 0 {
			return wo.focusField(j)
		}
		return nil
	}
	// Forward to text input, mirroring back into State.
	if cur < len(wo.inputs) {
		var cmd tea.Cmd
		wo.inputs[cur], cmd = wo.inputs[cur].Update(msg)
		wo.touched[cur] = true
		wo.state.SetText(cur, wo.inputs[cur].Value())
		return cmd
	}
	return nil
}

func (wo *wizardOverlay) Box(w, h int) string {
	width := w * 2 / 3
	if wo.preamble != "" {
		width = w - 8 // use most of the terminal for preamble content
	}
	if width < 50 {
		width = 50
	}
	if width > w-4 {
		width = w - 4
	}

	opName := ""
	if wo.op != nil {
		opName = wo.op.Name
	}

	// Busy state: wizard was submitted; we're waiting for the saga to
	// either ask for more input or complete. Render a spinner instead
	// of the (now stale) form fields so users get continuous feedback.
	if wo.busy {
		body := theme.Heading.Render(opName) + "\n\n" +
			"  " + wo.spin.View() + " " + theme.MutedText.Render("Working…") + "\n"
		return theme.Border.Width(width).Render(body)
	}

	var body strings.Builder
	body.WriteString(theme.Heading.Render(opName) + "\n\n")

	// Render scrollable preamble (e.g. IAM policy review).
	if wo.preamble != "" {
		pLines := strings.Split(strings.TrimRight(wo.preamble, "\n"), "\n")
		// Truncate long lines to fit inside the box. The inner
		// content area is width minus border (2) and indent (2).
		maxLineW := width - 6
		if maxLineW < 20 {
			maxLineW = 20
		}
		for i, ln := range pLines {
			if len(ln) > maxLineW {
				pLines[i] = ln[:maxLineW-1] + "…"
			}
		}
		// Reserve space for: heading(2) + fields + field spacers +
		// hint(2) + border(2) + scroll indicators(2) + breathing room.
		visibleCount := 0
		for i := range wo.fields {
			if wo.fieldVisible(i) {
				visibleCount++
			}
		}
		overhead := 2 + visibleCount*2 + 2 + 2 + 2 + 2
		maxPreambleH := h - overhead
		if maxPreambleH < 3 {
			maxPreambleH = 3
		}
		// Clamp scroll offset.
		maxScroll := len(pLines) - maxPreambleH
		if maxScroll < 0 {
			maxScroll = 0
		}
		if wo.preambleScroll > maxScroll {
			wo.preambleScroll = maxScroll
		}
		if wo.preambleScroll < 0 {
			wo.preambleScroll = 0
		}
		end := wo.preambleScroll + maxPreambleH
		if end > len(pLines) {
			end = len(pLines)
		}
		if wo.preambleScroll > 0 {
			body.WriteString(theme.MutedText.Render(fmt.Sprintf("  ↑ %d more lines (pgup)", wo.preambleScroll)) + "\n")
		}
		for _, ln := range pLines[wo.preambleScroll:end] {
			body.WriteString("  " + ln + "\n")
		}
		if end < len(pLines) {
			body.WriteString(theme.MutedText.Render(fmt.Sprintf("  ↓ %d more lines (pgdown)", len(pLines)-end)) + "\n")
		}
		body.WriteString("\n")
	}

	// Find the last visible field so we only add spacer newlines
	// between visible rows (not between the last and a run of
	// hidden ones).
	cur := wo.idx()
	lastVisible := wo.lastVisible()

	for i, f := range wo.fields {
		// Hidden by a When predicate: don't render at all.
		if !wo.fieldVisible(i) {
			continue
		}
		focused := i == cur
		// DisplayLabel strips internal prefixes (e.g. "__ni/name"
		// renders as "Instance name") so sub-saga namespacing
		// doesn't leak into the TUI.
		labelText := f.DisplayLabel()
		var label string
		if focused {
			label = theme.Value.Render(labelText)
		} else {
			label = theme.Label.Render(labelText)
		}
		if f.Required {
			label += theme.Err.Render(" *")
		}

		entry := wo.state.Entry(i)

		// Unfocused fields collapse to label + current value (one line).
		if !focused {
			val := wo.fieldValue(i)
			if wo.isMulti(i) {
				n := len(entry.MultiSel)
				switch n {
				case 0:
					val = theme.MutedText.Render("(none selected)")
				case 1:
				default:
					val = fmt.Sprintf("%d selected", n)
				}
			} else if val == "" {
				val = theme.MutedText.Render("—")
			} else if wo.handoff && entry.Defaulted && !entry.Committed && !wo.isSelect(i) {
				val = theme.MutedText.Render(val + " (default)")
			}
			body.WriteString("  " + label + "  " + val + "\n")
			if entry.Err != "" {
				body.WriteString("  " + theme.Err.Render(entry.Err) + "\n")
			}
			continue
		}

		body.WriteString("  " + label + "\n")

		// Cap choice lists to fit the terminal.
		maxChoices := h - len(wo.fields) - 8 // room for other fields + chrome
		if maxChoices < 5 {
			maxChoices = 5
		}

		switch {
		case wo.isSelect(i) && entry.Loading:
			body.WriteString("  " + wo.spin.View() + " " + theme.MutedText.Render("loading…") + "\n")
		case wo.isSelect(i):
			body.WriteString(wo.renderChoices(i, focused, maxChoices))
		case wo.isFile(i):
			body.WriteString(wo.renderFile(i, focused))
		default:
			body.WriteString("  " + wo.inputs[i].View() + "\n")
		}

		if entry.Err != "" {
			body.WriteString("  " + theme.Err.Render(entry.Err) + "\n")
		}
		if i < lastVisible {
			body.WriteString("\n")
		}
	}

	hint := "tab/shift+tab · enter: next (or submit on last field) · esc cancel"
	if wo.isMulti(cur) {
		hint = "space toggle · ↑/↓ move · enter: next (or submit on last field) · esc cancel"
	}
	body.WriteString("\n" + theme.MutedText.Render("  "+hint))

	// Clamp body height to fit the terminal (border adds 2 rows).
	rendered := body.String()
	if maxH := h - 4; maxH > 0 {
		lines := strings.Split(rendered, "\n")
		if len(lines) > maxH {
			rendered = strings.Join(lines[:maxH], "\n")
		}
	}
	return theme.Border.Width(width).MaxWidth(width + 2).Render(rendered)
}

func (wo *wizardOverlay) renderFile(fieldIdx int, focused bool) string {
	fp := wo.pickers[fieldIdx]
	if fp == nil {
		return ""
	}
	path := fp.Path
	if path == "" {
		path = theme.MutedText.Render("(choose a file — j/k ↑/↓ · enter select/open · h back)")
	} else if focused {
		path = theme.Value.Render(path)
	}
	marker := "  "
	if focused {
		marker = "  " + theme.Key.Render("▸ ")
	}
	// Render the filepicker body indented. The picker handles its own
	// window of files — limit visible height in Show().
	inner := "\n  " + strings.ReplaceAll(fp.View(), "\n", "\n  ") + "\n"
	if !focused {
		// When not focused, collapse to just the selected path.
		inner = "\n"
	}
	return marker + "selected: " + path + inner
}

func (wo *wizardOverlay) renderChoices(fieldIdx int, focused bool, maxVisible int) string {
	entry := wo.state.Entry(fieldIdx)
	cs := entry.Choices
	if len(cs) == 0 {
		return "  " + theme.MutedText.Render("(no options)") + "\n"
	}
	sel := entry.SelIdx
	multi := wo.isMulti(fieldIdx)

	// Compute scroll window around the selected item.
	start, end := 0, len(cs)
	if len(cs) > maxVisible {
		half := maxVisible / 2
		start = sel - half
		if start < 0 {
			start = 0
		}
		end = start + maxVisible
		if end > len(cs) {
			end = len(cs)
			start = end - maxVisible
		}
	}

	var s strings.Builder
	// Column header line (only for Multi fields where Display is a
	// table-formatted row with a Help-formatted header). Indent
	// matches the checkbox column below so cells line up.
	if multi && focused && cs[0].Help != "" {
		s.WriteString("      " + theme.Label.Render(cs[0].Help) + "\n")
	}
	if start > 0 {
		s.WriteString("    " + theme.MutedText.Render(fmt.Sprintf("↑ %d more", start)) + "\n")
	}
	for i := start; i < end; i++ {
		c := cs[i]
		marker := "    "
		line := c.Display
		if line == "" {
			line = c.Value
			if !multi && c.Help != "" {
				line += theme.MutedText.Render("  " + c.Help)
			}
		}
		// Compose the checkbox/marker column for multi mode. Keeps
		// visual alignment across rows whether or not a choice is
		// selected. Single-select mode preserves the pre-existing
		// "▸ " highlight for the cursor row.
		if multi {
			box := "[ ]"
			if entry.MultiSel[i] {
				box = theme.Value.Render("[x]")
			}
			if i == sel && focused {
				marker = "  " + theme.Key.Render("▸") + " " + box + " "
				line = theme.Value.Render(line)
			} else if i == sel {
				marker = "  › " + box + " "
			} else {
				marker = "    " + box + " "
			}
		} else if i == sel {
			if focused {
				marker = "  " + theme.Key.Render("▸ ")
				line = theme.Value.Render(line)
			} else {
				marker = "  › "
			}
		}
		s.WriteString(marker + line + "\n")
	}
	if end < len(cs) {
		s.WriteString("    " + theme.MutedText.Render(fmt.Sprintf("↓ %d more", len(cs)-end)) + "\n")
	}
	// Footer hint tailored to multi vs single. Rendered only for the
	// focused field so unfocused collapsed rows stay compact.
	if focused && multi {
		n := len(entry.MultiSel)
		s.WriteString("  " + theme.MutedText.Render(fmt.Sprintf("%d selected · space toggle · enter confirm", n)) + "\n")
	}
	return s.String()
}
