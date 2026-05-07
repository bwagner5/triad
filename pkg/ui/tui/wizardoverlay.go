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
)

// wizardOverlay is a form overlay that shows all fields at once.
// The focused field is editable; tab/shift-tab moves between fields.
type wizardOverlay struct {
	active   bool
	ctx      context.Context
	resource *registry.Resource
	op       *registry.Operation
	fields   []registry.Field
	input    registry.Input
	idx      int // focused field

	inputs  []textinput.Model   // one per field
	pickers []*filepicker.Model // one per file-field, nil otherwise
	spin    spinner.Model
	loading []bool              // per-field loading state
	choices [][]registry.Choice // per-field choices
	selIdx  []int               // per-field selection cursor
	// multiSel tracks selected choice indices for Multi fields.
	// nil for non-Multi fields. Mutations go through toggleMulti
	// so fieldValue can commit a stable, sorted, comma-joined result.
	multiSel []map[int]bool

	errs []string // per-field validation error
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
	wo.active = true
	wo.busy = false
	wo.ctx = ctx
	wo.resource = res
	wo.op = op
	wo.fields = fields
	wo.input = input
	wo.idx = 0
	wo.preamble = preamble
	wo.preambleScroll = 0

	n := len(fields)
	wo.inputs = make([]textinput.Model, n)
	wo.pickers = make([]*filepicker.Model, n)
	wo.loading = make([]bool, n)
	wo.choices = make([][]registry.Choice, n)
	wo.selIdx = make([]int, n)
	wo.multiSel = make([]map[int]bool, n)
	wo.errs = make([]string, n)

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
		// Seed text inputs with precedence: Input value > lazy Prefill >
		// static Default. Ensures the wizard reflects what'll actually
		// run — the user sees 'dev' for an env field with Default='dev'
		// regardless of whether they reached the wizard via CLI flags
		// (bindFields already populated Input) or TUI key binding (Input
		// empty; Default wasn't applied). Suggest fields read from
		// choices, not text input, so skip them.
		if f.Suggest == nil {
			var seed string
			if v, ok := input[f.Flag]; ok && v != "" {
				seed = v
			} else if f.Prefill != nil {
				seed = f.Prefill()
			} else if f.Default != nil {
				seed = fmt.Sprintf("%v", f.Default)
			}
			if seed != "" {
				ti.SetValue(seed)
			}
		}
		wo.inputs[i] = ti

		if f.Suggest != nil {
			wo.loading[i] = true
			cmds = append(cmds, wo.fetchSuggest(i, f))
		}
	}
	cmds = append(cmds, wo.focusField(0), wo.spin.Tick)
	// If the first field is hidden by its When predicate, hop to
	// the first visible one. Evaluated after all inputs have been
	// seeded so When predicates reading seeded defaults see them.
	if !wo.fieldVisible(0) {
		if j := wo.firstVisible(); j >= 0 {
			cmds = append(cmds, wo.focusField(j))
		}
	}
	return tea.Batch(cmds...)
}

func (wo *wizardOverlay) Clear() {
	wo.active = false
	wo.busy = false
	wo.fields = nil
	wo.inputs = nil
	wo.pickers = nil
	wo.choices = nil
	wo.multiSel = nil
	wo.errs = nil
	wo.preamble = ""
	wo.preambleScroll = 0
}

func (wo *wizardOverlay) isSelect(i int) bool {
	return i < len(wo.fields) && wo.fields[i].Suggest != nil
}

// isMulti reports whether field i is a multi-select Suggest field.
func (wo *wizardOverlay) isMulti(i int) bool {
	return wo.isSelect(i) && wo.fields[i].Multi
}

// toggleMulti flips the selected state of choice `choiceIdx` for field i.
// Allocates the set on first use so fieldValue can distinguish "never
// touched" from "explicitly empty".
func (wo *wizardOverlay) toggleMulti(i, choiceIdx int) {
	if i >= len(wo.multiSel) {
		return
	}
	if wo.multiSel[i] == nil {
		wo.multiSel[i] = map[int]bool{}
	}
	if wo.multiSel[i][choiceIdx] {
		delete(wo.multiSel[i], choiceIdx)
	} else {
		wo.multiSel[i][choiceIdx] = true
	}
}

func (wo *wizardOverlay) isFile(i int) bool {
	return i < len(wo.fields) && (wo.fields[i].File || wo.fields[i].EffectiveKind() == registry.KindFile)
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
	// Commit the departing field's value into wo.input so that
	// liveInputForWhen (which only reads the focused Suggest field's
	// cursor) still sees the answer after focus moves away. Without
	// this, navigating away from create-new-instance=true would
	// cause __ni/* fields to vanish because the unfocused Suggest
	// value is excluded from When evaluation.
	if wo.idx >= 0 && wo.idx < len(wo.fields) {
		if v := wo.fieldValue(wo.idx); v != "" {
			wo.input[wo.fields[wo.idx].Flag] = v
		}
	}
	wo.idx = i
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

// liveInput returns a snapshot of the overlay's answers so far: the
// caller-supplied Input, overlaid with each field's current widget
// value. Used by submit() to commit final values.
func (wo *wizardOverlay) liveInput() registry.Input {
	out := registry.Input{}
	for k, v := range wo.input {
		out[k] = v
	}
	for i, f := range wo.fields {
		if v := wo.fieldValue(i); v != "" {
			out[f.Flag] = v
		}
	}
	return out
}

// liveInputForWhen builds the input snapshot used to evaluate When
// predicates for field excludeIdx. Rules:
//
//  1. Field excludeIdx's own value is always excluded (both from
//     wo.input and widget state) — a field must not hide itself.
//  2. Suggest fields only contribute their cursor value when they
//     appear BEFORE excludeIdx in field order. This mirrors the CLI
//     wizard's sequential model: earlier answers drive later fields'
//     visibility, but a later field's uncommitted cursor can't hide
//     an earlier one (which would block shift+tab navigation).
//  3. Text inputs and file pickers always contribute.
//  4. Committed values in wo.input (from conf, flags, Pre hooks, or
//     prior focusField commits) contribute for all fields except
//     excludeIdx.
func (wo *wizardOverlay) liveInputForWhen(excludeIdx int) registry.Input {
	out := registry.Input{}
	excludeFlag := ""
	if excludeIdx >= 0 && excludeIdx < len(wo.fields) {
		excludeFlag = wo.fields[excludeIdx].Flag
	}
	for k, v := range wo.input {
		if k == excludeFlag {
			continue
		}
		out[k] = v
	}
	for i, f := range wo.fields {
		if i == excludeIdx {
			continue
		}
		// Suggest fields: only include if they precede the field
		// being evaluated (sequential "already answered" model).
		if wo.isSelect(i) && i >= excludeIdx {
			continue
		}
		if v := wo.fieldValue(i); v != "" {
			out[f.Flag] = v
		}
	}
	return out
}

// fieldVisible reports whether field i should be shown in the overlay.
// A field is hidden when its When predicate returns false against the
// live input. No predicate means always visible.
//
// Uses liveInputForWhen which excludes field i's own widget value and
// only includes the focused Suggest field's cursor — unfocused Suggest
// fields with loaded but uncommitted choices don't pollute evaluation.
func (wo *wizardOverlay) fieldVisible(i int) bool {
	if i < 0 || i >= len(wo.fields) {
		return false
	}
	f := wo.fields[i]
	if f.When == nil {
		return true
	}
	return f.When(wo.liveInputForWhen(i))
}

// nextVisible returns the index of the first visible field strictly
// after i, or -1 if none. firstVisible returns the first visible
// field in the overlay (or -1 if all are hidden, which shouldn't
// happen in practice).
func (wo *wizardOverlay) nextVisible(i int) int {
	for j := i + 1; j < len(wo.fields); j++ {
		if wo.fieldVisible(j) {
			return j
		}
	}
	return -1
}

func (wo *wizardOverlay) prevVisible(i int) int {
	for j := i - 1; j >= 0; j-- {
		if wo.fieldVisible(j) {
			return j
		}
	}
	return -1
}

func (wo *wizardOverlay) firstVisible() int {
	for j := 0; j < len(wo.fields); j++ {
		if wo.fieldVisible(j) {
			return j
		}
	}
	return -1
}

func (wo *wizardOverlay) lastVisible() int {
	for j := len(wo.fields) - 1; j >= 0; j-- {
		if wo.fieldVisible(j) {
			return j
		}
	}
	return -1
}

func (wo *wizardOverlay) submit() tea.Cmd {
	// Validate visible fields only. Hidden fields (When→false) are
	// conceptually absent — don't enforce their Required, don't
	// validate stale widget state, and don't carry their values
	// forward into Input. This mirrors wizard.Collect's
	// startField() behavior so both UIs agree on what "present"
	// means.
	valid := true
	for i, f := range wo.fields {
		wo.errs[i] = ""
		if !wo.fieldVisible(i) {
			continue
		}
		val := wo.fieldValue(i)
		if f.Required && val == "" {
			wo.errs[i] = "required"
			valid = false
		} else if val != "" {
			if err := f.ValidateValue(val); err != nil {
				wo.errs[i] = err.Error()
				valid = false
			}
		}
	}
	if !valid {
		// Focus first visible field with error.
		for i, e := range wo.errs {
			if e != "" && wo.fieldVisible(i) {
				return wo.focusField(i)
			}
		}
		return nil
	}
	for i, f := range wo.fields {
		if wo.fieldVisible(i) {
			wo.input[f.Flag] = wo.fieldValue(i)
		} else {
			// Defensive: if the predicate flipped after seeding
			// we might have a stale value in Input. Drop it so
			// downstream saga steps don't see "ghost" answers
			// (e.g. __ni/region when the user chose existing).
			delete(wo.input, f.Flag)
		}
	}
	// Keep the overlay active showing a "working" spinner so the user
	// gets continuous visual feedback instead of a flash-then-next-screen.
	// The next Show() / Clear() supersedes this.
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

func (wo *wizardOverlay) fieldValue(i int) string {
	if wo.isMulti(i) {
		if i >= len(wo.multiSel) || len(wo.multiSel[i]) == 0 {
			return ""
		}
		// Emit in choice order (not selection order) for stable output.
		var picks []string
		for idx, c := range wo.choices[i] {
			if wo.multiSel[i][idx] {
				picks = append(picks, c.Value)
			}
		}
		return strings.Join(picks, ",")
	}
	if wo.isSelect(i) {
		if len(wo.choices[i]) > 0 && wo.selIdx[i] < len(wo.choices[i]) {
			return wo.choices[i][wo.selIdx[i]].Value
		}
		return ""
	}
	if wo.isFile(i) && wo.pickers[i] != nil {
		return wo.pickers[i].Path
	}
	return wo.inputs[i].Value()
}

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
	// If the focused field is a filepicker, let it handle keystrokes.
	// handleKey below still intercepts tab/shift-tab/enter at submit time.
	if wo.isFile(wo.idx) && wo.pickers[wo.idx] != nil {
		if key, ok := msg.(tea.KeyPressMsg); ok {
			if consumed, cmd := wo.fileKey(key); consumed {
				return true, cmd
			}
		}
		fp := *wo.pickers[wo.idx]
		newFp, cmd := fp.Update(msg)
		wo.pickers[wo.idx] = &newFp
		if didSelect, path := newFp.DidSelectFile(msg); didSelect {
			wo.pickers[wo.idx].Path = path
			// File selected: advance to next visible field if there
			// is one, else submit. Prevents accidental submit when
			// the user expects enter to just commit the file.
			if j := wo.nextVisible(wo.idx); j >= 0 {
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
			wo.loading[msg.idx] = false
			if msg.err != nil {
				wo.errs[msg.idx] = msg.err.Error()
			} else {
				wo.choices[msg.idx] = msg.choices
				// Seed cursor to the saved/prefilled/default answer
				// when one of the returned choices matches. Mirrors
				// the text-input seeding (Input > Prefill > Default)
				// so confirmation prompts can default to "Yes" and
				// users who already stated intent don't have to
				// arrow-down before pressing enter.
				f := wo.fields[msg.idx]
				var want string
				if v, ok := wo.input[f.Flag]; ok && v != "" {
					want = v
				} else if f.Prefill != nil {
					want = f.Prefill()
				} else if f.Default != nil {
					want = fmt.Sprintf("%v", f.Default)
				}
				if want != "" {
					if wo.isMulti(msg.idx) {
						// Multi seeding: every matching value becomes a
						// pre-checked row. Parsing here (instead of via
						// Input.Multi) keeps this package free of import
						// cycles on the commasplit helper.
						set := map[string]bool{}
						for _, v := range strings.Split(want, ",") {
							v = strings.TrimSpace(v)
							if v != "" {
								set[v] = true
							}
						}
						sel := map[int]bool{}
						for i, c := range msg.choices {
							if set[c.Value] {
								sel[i] = true
							}
						}
						if len(sel) > 0 {
							wo.multiSel[msg.idx] = sel
						}
					} else {
						for i, c := range msg.choices {
							if c.Value == want {
								wo.selIdx[msg.idx] = i
								break
							}
						}
					}
				}
			}
		}
		return true, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		wo.spin, cmd = wo.spin.Update(msg)
		return true, cmd
	}
	// Forward to focused text input.
	if wo.idx < len(wo.inputs) && !wo.isSelect(wo.idx) && !wo.isFile(wo.idx) {
		var cmd tea.Cmd
		wo.inputs[wo.idx], cmd = wo.inputs[wo.idx].Update(msg)
		return true, cmd
	}
	return true, nil
}

// fileKey handles keys that must act on the overlay even when a filepicker
// is focused (tab/shift-tab/esc, and enter-to-submit once a file has
// been selected). Returns consumed=true when we handled it.
func (wo *wizardOverlay) fileKey(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		op, res := wo.op, wo.resource
		wo.Clear()
		return true, func() tea.Msg { return wizardDoneMsg{resource: res, op: op, input: nil} }
	case "tab":
		if j := wo.nextVisible(wo.idx); j >= 0 {
			return true, wo.focusField(j)
		}
		return true, nil
	case "shift+tab":
		if j := wo.prevVisible(wo.idx); j >= 0 {
			return true, wo.focusField(j)
		}
		return true, nil
	case "enter":
		// Enter is overloaded: the filepicker uses it to open a directory
		// OR select a file. Only one of those is "commit the field".
		// If the picker already has a Path committed, advance to next
		// visible field (or submit when on the last visible field).
		if wo.pickers[wo.idx] != nil && wo.pickers[wo.idx].Path != "" {
			if j := wo.nextVisible(wo.idx); j >= 0 {
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
	trace.Trace(wo.ctx, "tui wizard key",
		"key", key, "idx", wo.idx, "isSelect", wo.isSelect(wo.idx),
		"loading", wo.idx < len(wo.loading) && wo.loading[wo.idx],
		"choices", len(wo.choices[wo.idx]), "selIdx", wo.selIdx[wo.idx],
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
		if j := wo.nextVisible(wo.idx); j >= 0 {
			return wo.focusField(j)
		}
		return nil
	case "shift+tab":
		if j := wo.prevVisible(wo.idx); j >= 0 {
			return wo.focusField(j)
		}
		return nil
	case "enter":
		// Enter on a non-last visible field advances to the next
		// visible one (natural typing flow). Enter on the last
		// visible field submits. For select fields, enter on the
		// last visible field also submits; on non-last, it commits
		// the selection and advances.
		if j := wo.nextVisible(wo.idx); j >= 0 {
			// Run field-level validation before advancing so the
			// user doesn't tab past a bad value unknowingly.
			if err := wo.validateField(wo.idx); err != nil {
				wo.errs[wo.idx] = err.Error()
				return nil
			}
			wo.errs[wo.idx] = ""
			return wo.focusField(j)
		}
		return wo.submit()
	}
	// Selection list navigation for Suggest fields.
	// up/down/j/k all move the list cursor. If the current field is a text
	// input, up/down act as tab/shift-tab (field nav).
	if wo.isSelect(wo.idx) && !wo.loading[wo.idx] {
		switch key {
		case "j", "down":
			if wo.selIdx[wo.idx] < len(wo.choices[wo.idx])-1 {
				wo.selIdx[wo.idx]++
			}
			return nil
		case "k", "up":
			if wo.selIdx[wo.idx] > 0 {
				wo.selIdx[wo.idx]--
			}
			return nil
		case " ", "space":
			// Space toggles selection only for Multi fields.
			// Regular Suggest fields ignore it so typing into a
			// would-be text input isn't mistaken for selection.
			if wo.isMulti(wo.idx) && len(wo.choices[wo.idx]) > 0 {
				wo.toggleMulti(wo.idx, wo.selIdx[wo.idx])
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
		if j := wo.nextVisible(wo.idx); j >= 0 {
			return wo.focusField(j)
		}
		return nil
	case "up":
		if j := wo.prevVisible(wo.idx); j >= 0 {
			return wo.focusField(j)
		}
		return nil
	}
	// Forward to text input.
	if wo.idx < len(wo.inputs) {
		var cmd tea.Cmd
		wo.inputs[wo.idx], cmd = wo.inputs[wo.idx].Update(msg)
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
	lastVisible := wo.lastVisible()

	for i, f := range wo.fields {
		// Hidden by a When predicate: don't render at all.
		if !wo.fieldVisible(i) {
			continue
		}
		focused := i == wo.idx
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

		// Unfocused fields collapse to label + current value (one line).
		if !focused {
			val := wo.fieldValue(i)
			if wo.isMulti(i) {
				n := 0
				if i < len(wo.multiSel) {
					n = len(wo.multiSel[i])
				}
				switch n {
				case 0:
					val = theme.MutedText.Render("(none selected)")
				case 1:
					val = val // single pick: show the value verbatim
				default:
					val = fmt.Sprintf("%d selected", n)
				}
			} else if val == "" {
				val = theme.MutedText.Render("—")
			}
			body.WriteString("  " + label + "  " + val + "\n")
			if wo.errs[i] != "" {
				body.WriteString("  " + theme.Err.Render(wo.errs[i]) + "\n")
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
		case wo.isSelect(i) && wo.loading[i]:
			body.WriteString("  " + wo.spin.View() + " " + theme.MutedText.Render("loading…") + "\n")
		case wo.isSelect(i):
			body.WriteString(wo.renderChoices(i, focused, maxChoices))
		case wo.isFile(i):
			body.WriteString(wo.renderFile(i, focused))
		default:
			body.WriteString("  " + wo.inputs[i].View() + "\n")
		}

		if wo.errs[i] != "" {
			body.WriteString("  " + theme.Err.Render(wo.errs[i]) + "\n")
		}
		if i < lastVisible {
			body.WriteString("\n")
		}
	}

	hint := "tab/shift+tab · enter: next (or submit on last field) · esc cancel"
	if wo.isMulti(wo.idx) {
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
	cs := wo.choices[fieldIdx]
	if len(cs) == 0 {
		return "  " + theme.MutedText.Render("(no options)") + "\n"
	}
	sel := wo.selIdx[fieldIdx]
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
			if wo.multiSel[fieldIdx][i] {
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
		n := len(wo.multiSel[fieldIdx])
		s.WriteString("  " + theme.MutedText.Render(fmt.Sprintf("%d selected · space toggle · enter confirm", n)) + "\n")
	}
	return s.String()
}
