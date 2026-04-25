package tui

import (
	"context"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/filepicker"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
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

	errs []string // per-field validation error
	w, h int
}

func newWizardOverlay() wizardOverlay {
	return wizardOverlay{spin: spinner.New()}
}

func (wo *wizardOverlay) Active() bool     { return wo.active }
func (wo *wizardOverlay) SetSize(w, h int) { wo.w, wo.h = w, h }

func (wo *wizardOverlay) Show(ctx context.Context, res *registry.Resource, op *registry.Operation, fields []registry.Field, input registry.Input) tea.Cmd {
	wo.active = true
	wo.ctx = ctx
	wo.resource = res
	wo.op = op
	wo.fields = fields
	wo.input = input
	wo.idx = 0

	n := len(fields)
	wo.inputs = make([]textinput.Model, n)
	wo.pickers = make([]*filepicker.Model, n)
	wo.loading = make([]bool, n)
	wo.choices = make([][]registry.Choice, n)
	wo.selIdx = make([]int, n)
	wo.errs = make([]string, n)

	var cmds []tea.Cmd
	for i, f := range fields {
		if f.File {
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
		// Seed text inputs with the field's lazy Prefill so the user can
		// press Enter to accept a sensible default (git repo name, cwd,
		// etc.) without making it count as Required-satisfied.
		if f.Prefill != nil && f.Suggest == nil {
			if v := f.Prefill(); v != "" {
				ti.SetValue(v)
			}
		}
		wo.inputs[i] = ti

		if f.Suggest != nil {
			wo.loading[i] = true
			cmds = append(cmds, wo.fetchSuggest(i, f))
		}
	}
	cmds = append(cmds, wo.inputs[0].Focus(), wo.spin.Tick)
	return tea.Batch(cmds...)
}

func (wo *wizardOverlay) Clear() {
	wo.active = false
	wo.fields = nil
	wo.inputs = nil
	wo.pickers = nil
	wo.choices = nil
	wo.errs = nil
}

func (wo *wizardOverlay) isSelect(i int) bool {
	return i < len(wo.fields) && wo.fields[i].Suggest != nil
}

func (wo *wizardOverlay) isFile(i int) bool {
	return i < len(wo.fields) && wo.fields[i].File
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

func (wo *wizardOverlay) submit() tea.Cmd {
	// Validate all fields.
	valid := true
	for i, f := range wo.fields {
		val := wo.fieldValue(i)
		wo.errs[i] = ""
		if f.Required && val == "" {
			wo.errs[i] = "required"
			valid = false
		} else if val != "" && f.Validate != nil {
			if err := f.Validate(val); err != nil {
				wo.errs[i] = err.Error()
				valid = false
			}
		}
	}
	if !valid {
		// Focus first field with error.
		for i, e := range wo.errs {
			if e != "" {
				return wo.focusField(i)
			}
		}
		return nil
	}
	for i, f := range wo.fields {
		wo.input[f.Flag] = wo.fieldValue(i)
	}
	wo.active = false
	return func() tea.Msg {
		return wizardDoneMsg{resource: wo.resource, op: wo.op, input: wo.input}
	}
}

func (wo *wizardOverlay) fieldValue(i int) string {
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
			// Auto-submit on select: the user pressed enter on a file,
			// we got the commit, advance the wizard. If other required
			// fields are still empty, submit() will focus the first one.
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
		if wo.idx < len(wo.fields)-1 {
			return true, wo.focusField(wo.idx + 1)
		}
		return true, nil
	case "shift+tab":
		if wo.idx > 0 {
			return true, wo.focusField(wo.idx - 1)
		}
		return true, nil
	case "enter":
		// Enter is overloaded: the filepicker uses it to open a directory
		// OR select a file. Only one of those is "submit the wizard".
		// If the picker already has a Path committed, treat enter as
		// submit; otherwise let the picker handle it.
		if wo.pickers[wo.idx] != nil && wo.pickers[wo.idx].Path != "" {
			return true, wo.submit()
		}
		return false, nil
	}
	return false, nil
}

func (wo *wizardOverlay) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	key := msg.String()
	trace.Log("tui.wizard.key",
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
	case "tab":
		if wo.idx < len(wo.fields)-1 {
			return wo.focusField(wo.idx + 1)
		}
		return nil
	case "shift+tab":
		if wo.idx > 0 {
			return wo.focusField(wo.idx - 1)
		}
		return nil
	case "enter":
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
		}
		return nil // consume other keys on select fields
	}
	// Text input field: up/down navigate between fields.
	switch key {
	case "down":
		if wo.idx < len(wo.fields)-1 {
			return wo.focusField(wo.idx + 1)
		}
		return nil
	case "up":
		if wo.idx > 0 {
			return wo.focusField(wo.idx - 1)
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

	var body strings.Builder
	body.WriteString(theme.Heading.Render(opName) + "\n\n")

	for i, f := range wo.fields {
		focused := i == wo.idx
		label := f.Flag
		if focused {
			label = theme.Value.Render(label)
		} else {
			label = theme.Label.Render(label)
		}
		if f.Required {
			label += theme.Err.Render(" *")
		}
		body.WriteString("  " + label + "\n")

		switch {
		case wo.isSelect(i) && wo.loading[i]:
			body.WriteString("  " + wo.spin.View() + " " + theme.MutedText.Render("loading…") + "\n")
		case wo.isSelect(i):
			body.WriteString(wo.renderChoices(i, focused))
		case wo.isFile(i):
			body.WriteString(wo.renderFile(i, focused))
		default:
			body.WriteString("  " + wo.inputs[i].View() + "\n")
		}

		if wo.errs[i] != "" {
			body.WriteString("  " + theme.Err.Render(wo.errs[i]) + "\n")
		}
		if i < len(wo.fields)-1 {
			body.WriteString("\n")
		}
	}

	body.WriteString("\n" + theme.MutedText.Render("  tab/shift+tab navigate · enter submit · esc cancel"))

	return theme.Border.Width(width).Render(body.String())
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

func (wo *wizardOverlay) renderChoices(fieldIdx int, focused bool) string {
	cs := wo.choices[fieldIdx]
	if len(cs) == 0 {
		return "  " + theme.MutedText.Render("(no options)") + "\n"
	}
	sel := wo.selIdx[fieldIdx]
	var s strings.Builder
	for i, c := range cs {
		marker := "    "
		line := c.Display
		if line == "" {
			line = c.Value
			if c.Help != "" {
				line += theme.MutedText.Render("  " + c.Help)
			}
		}
		if i == sel {
			if focused {
				marker = "  " + theme.Key.Render("▸ ")
				line = theme.Value.Render(line)
			} else {
				marker = "  › "
			}
		}
		s.WriteString(marker + line + "\n")
	}
	return s.String()
}
