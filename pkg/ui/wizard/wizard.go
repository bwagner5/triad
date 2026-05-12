// Package wizard is an inline (non-alt-screen) bubbletea v2 flow that
// prompts for missing required Fields. Populated answers are written back
// into the provided registry.Input.
//
// Fields with a Suggest function render as a single-selection list; fields
// without one render as a text input.
//
// Per-field state (answers, choice cursors, multi-select checkboxes,
// loaded Suggest choices, the "already answered" history) lives in
// wizardstate.State, which the TUI wizard overlay also uses. Sharing
// State means both UIs agree on visibility cascades, GoBack semantics,
// and submit behavior, and a future Ctrl+T handoff can swap UIs
// mid-run without losing the user's progress.
package wizard

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
	"github.com/bwagner5/triad/pkg/ui/theme"
	"github.com/bwagner5/triad/pkg/ui/wizardstate"
)

// Collect prompts for each Field and writes answers into in.
// Runs inline (no alt-screen) so output blends with the surrounding CLI.
func Collect(ctx context.Context, fields []registry.Field, in registry.Input) error {
	return CollectWithReason(ctx, "", fields, in)
}

// CollectWithReason is like Collect but prints reason above the first
// prompt. reason may contain newlines; it's rendered verbatim so
// callers can pre-format multi-section summaries.
func CollectWithReason(ctx context.Context, reason string, fields []registry.Field, in registry.Input) error {
	if len(fields) == 0 {
		return nil
	}
	m := newModel(ctx, fields, in)
	m.reason = reason
	p := tea.NewProgram(m, tea.WithContext(ctx))
	finalModel, err := p.Run()
	if err != nil {
		return err
	}
	fm := finalModel.(*model)
	if fm.err != nil {
		return fm.err
	}
	if fm.canceled {
		return fmt.Errorf("canceled")
	}
	fm.state.ApplyToInput(in)
	return nil
}

type model struct {
	ctx    context.Context
	fields []registry.Field
	in     registry.Input
	state  *wizardstate.State
	reason string // optional header printed once above the first prompt

	ti      textinput.Model
	fp      *filepicker.Model // non-nil when current field is File
	spin    spinner.Model
	loading bool
	termH   int // terminal height for scroll capping

	err      error
	canceled bool
}

func newModel(ctx context.Context, fields []registry.Field, in registry.Input) *model {
	ti := textinput.New()
	ti.Prompt = "› "
	sp := spinner.New()
	return &model{
		ctx:    ctx,
		fields: fields,
		in:     in,
		state:  wizardstate.New(fields, in),
		ti:     ti,
		spin:   sp,
	}
}

// curField returns the field we're currently collecting.
func (m *model) curField() *registry.Field {
	idx := m.state.Idx()
	if idx >= len(m.fields) {
		return nil
	}
	return &m.fields[idx]
}

// isSelect returns true when the current field should be rendered as a list.
func (m *model) isSelect() bool {
	f := m.curField()
	return f != nil && f.Suggest != nil
}

// isMulti returns true when the current field is a multi-select list.
func (m *model) isMulti() bool {
	f := m.curField()
	return f != nil && f.Suggest != nil && f.Multi
}

// isFile returns true when the current field should be rendered as a file picker.
func (m *model) isFile() bool {
	f := m.curField()
	return f != nil && (f.File || f.EffectiveKind() == registry.KindFile)
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(m.startField(), m.spin.Tick)
}

// startField focuses the input and, if Suggest is set, kicks off a loader.
// State has already absorbed pre-seeded answers and skipped hidden
// fields' visibility checks, so this loop only advances past
// already-Committed entries (to preserve answers when the user
// shift+tabs forward through them) or hidden ones.
func (m *model) startField() tea.Cmd {
	idx := m.state.Idx()
	for idx < len(m.fields) {
		if !m.state.FieldVisible(idx) {
			idx++
			m.state.Focus(idx)
			continue
		}
		if m.state.Entry(idx).Committed {
			idx++
			m.state.Focus(idx)
			continue
		}
		break
	}
	f := m.curField()
	if f == nil {
		return tea.Quit
	}
	m.ti.Reset()
	m.fp = nil
	entry := m.state.Entry(idx)
	if m.isSelect() {
		// Selection mode: hide text input, fetch choices unless we
		// already have them (preserved across goBack).
		m.ti.Blur()
		if len(entry.Choices) == 0 {
			m.loading = true
			m.state.SetLoading(idx, true)
			return m.fetchSuggest(*f)
		}
		m.loading = false
		return nil
	}
	if m.isFile() {
		// File picker mode.
		fp := filepicker.New()
		fp.AllowedTypes = f.AllowedExts
		fp.ShowHidden = false
		fp.AutoHeight = false
		fp.SetHeight(10)
		// Restore the previously selected directory if the user is
		// returning to this field via shift+tab.
		if entry.FilePath != "" {
			fp.Path = entry.FilePath
		}
		if cwd, err := os.Getwd(); err == nil {
			fp.CurrentDirectory = cwd
		}
		m.fp = &fp
		m.ti.Blur()
		m.loading = false
		return fp.Init()
	}
	// Text input mode. State.Entry(idx).Text already carries either
	// the user's previous answer (preserved across shift+tab) or the
	// Input>Prefill>Default seed from state.New.
	m.loading = false
	m.ti.Placeholder = f.Help
	if seed := entry.Text; seed != "" {
		m.ti.SetValue(seed)
	}
	if f.Sensitive {
		m.ti.EchoMode = textinput.EchoPassword
	} else {
		m.ti.EchoMode = textinput.EchoNormal
	}
	return m.ti.Focus()
}

type suggestMsg struct {
	idx     int
	choices []registry.Choice
	err     error
}

func (m *model) fetchSuggest(f registry.Field) tea.Cmd {
	idx := m.state.Idx()
	return func() tea.Msg {
		cs, err := f.Suggest(m.ctx)
		return suggestMsg{idx: idx, choices: cs, err: err}
	}
}

// commitAndAdvance records val for the current field and moves to the next.
func (m *model) commitAndAdvance(val string) tea.Cmd {
	idx := m.state.Idx()
	f := m.curField()
	if f == nil {
		return tea.Quit
	}
	if err := m.state.CommitValue(idx, val); err != nil {
		m.err = err
		return nil
	}
	m.err = nil
	if next := m.state.NextVisible(idx); next >= 0 {
		m.state.Focus(next)
	} else {
		m.state.Focus(len(m.fields))
	}
	return m.startField()
}

// goBack returns to the previous answered field. State preserves the
// target's Text / SelIdx / MultiSel / FilePath so the user can edit
// their previous answer instead of retyping from default — the
// shift+tab "fix my typo" flow.
func (m *model) goBack() tea.Cmd {
	if m.state.GoBack(m.state.Idx()) < 0 {
		return nil
	}
	return m.startField()
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Forward non-key messages to the file picker when active.
	if m.isFile() && m.fp != nil {
		if _, isKey := msg.(tea.KeyPressMsg); !isKey {
			if _, isSuggest := msg.(suggestMsg); !isSuggest {
				if _, isTick := msg.(spinner.TickMsg); !isTick {
					fp := *m.fp
					newFp, cmd := fp.Update(msg)
					m.fp = &newFp
					return m, cmd
				}
			}
		}
	}
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			m.canceled = true
			return m, tea.Quit
		case "shift+tab":
			return m, m.goBack()
		case "tab":
			// Tab skips optional fields.
			if m.curField() != nil && !m.curField().Required {
				return m, m.commitAndAdvance("")
			}
		case "enter":
			if m.curField() == nil {
				return m, tea.Quit
			}
			if m.isFile() {
				// Enter on file picker: if a file is selected, commit it.
				// Otherwise let the picker handle it (directory open).
				if m.fp != nil && m.fp.Path != "" {
					return m, m.commitAndAdvance(m.fp.Path)
				}
				// Let the picker handle enter (open directory).
				fp := *m.fp
				newFp, cmd := fp.Update(msg)
				m.fp = &newFp
				if didSelect, path := newFp.DidSelectFile(msg); didSelect {
					m.fp.Path = path
					return m, m.commitAndAdvance(path)
				}
				return m, cmd
			}
			if m.isMulti() {
				if m.loading {
					return m, nil
				}
				idx := m.state.Idx()
				val := m.state.Value(idx)
				return m, m.commitAndAdvance(val)
			}
			if m.isSelect() {
				idx := m.state.Idx()
				e := m.state.Entry(idx)
				if m.loading || len(e.Choices) == 0 {
					return m, nil
				}
				return m, m.commitAndAdvance(e.Choices[e.SelIdx].Value)
			}
			val := m.ti.Value()
			if val == "" && m.curField().Required {
				return m, nil
			}
			return m, m.commitAndAdvance(val)
		case " ":
			// Space toggles the cursor row only for Multi fields.
			// Regular Suggest fields ignore it (so a user who starts
			// typing doesn't accidentally toggle a selection).
			if m.isMulti() && !m.loading {
				idx := m.state.Idx()
				e := m.state.Entry(idx)
				if len(e.Choices) > 0 {
					m.state.ToggleMulti(idx, e.SelIdx)
				}
				return m, nil
			}
		case "up", "k":
			if m.isSelect() {
				idx := m.state.Idx()
				e := m.state.Entry(idx)
				if e.SelIdx > 0 {
					m.state.SetSelIdx(idx, e.SelIdx-1)
				}
				return m, nil
			}
		case "down", "j":
			if m.isSelect() {
				idx := m.state.Idx()
				e := m.state.Entry(idx)
				if e.SelIdx < len(e.Choices)-1 {
					m.state.SetSelIdx(idx, e.SelIdx+1)
				}
				return m, nil
			}
		}
		// Forward key events to file picker when active.
		if m.isFile() && m.fp != nil {
			fp := *m.fp
			newFp, cmd := fp.Update(msg)
			m.fp = &newFp
			if didSelect, path := newFp.DidSelectFile(msg); didSelect {
				m.fp.Path = path
				return m, m.commitAndAdvance(path)
			}
			return m, cmd
		}
	case suggestMsg:
		if msg.idx != m.state.Idx() {
			return m, nil
		}
		m.loading = false
		if msg.err != nil {
			m.err = msg.err
			m.state.SetChoicesErr(msg.idx, msg.err)
			return m, nil
		}
		m.state.SetChoices(msg.idx, msg.choices)
		return m, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	case tea.WindowSizeMsg:
		m.termH = msg.Height
		return m, nil
	}
	if !m.isSelect() && !m.isFile() {
		var cmd tea.Cmd
		m.ti, cmd = m.ti.Update(msg)
		// Mirror in-flight typing into State so When predicates see
		// it (and so a goBack-then-forward roundtrip preserves the
		// user's typing).
		m.state.SetText(m.state.Idx(), m.ti.Value())
		return m, cmd
	}
	return m, nil
}

func (m *model) View() tea.View {
	var s string
	if m.reason != "" {
		// Prepend the reason block verbatim so callers can pre-format
		// multi-section summaries. Theme the header lightly so the
		// prompt itself stands out below it.
		s += m.reason
		if !strings.HasSuffix(m.reason, "\n") {
			s += "\n"
		}
		s += "\n"
	}
	// Render committed history, inserting a section header whenever
	// consecutive entries come from different sections.
	hist := m.state.CommittedHistory()
	prevSection := ""
	for _, h := range hist {
		if h.Section != "" && h.Section != prevSection {
			s += renderSectionHeader(h.Section) + "\n"
		}
		prevSection = h.Section
		labelText := h.Field.DisplayLabel()
		s += fmt.Sprintf("%s %s: %s\n",
			theme.OKMark, theme.Label.Render(labelText), h.DisplayVal)
	}
	f := m.curField()
	if f == nil {
		return tea.NewView(s)
	}
	// Section header for the current field, if this starts a new group.
	if f.Section != "" && f.Section != prevSection {
		s += renderSectionHeader(f.Section) + "\n"
	}
	// Preamble (multi-line verbatim text block). Dynamic variant
	// takes precedence so callers can pass a function computed from
	// the current Input (e.g. a review-and-confirm summary).
	preamble := f.Preamble
	if f.PreambleFunc != nil {
		preamble = f.PreambleFunc(m.state.LiveInput())
	}
	if preamble != "" {
		s += preamble
		if !strings.HasSuffix(preamble, "\n") {
			s += "\n"
		}
	}
	// Label: use title-cased Help (e.g. "App Name") when available,
	// falling back to a humanized flag name (strips __ns/ prefixes).
	labelText := f.DisplayLabel()
	label := theme.Label.Render(labelText)
	if f.Required {
		label += theme.Err.Render(" *")
	}
	label += theme.MutedText.Render(":")
	s += label + "\n"

	switch {
	case m.isSelect() && m.loading:
		s += m.spin.View() + " " + theme.MutedText.Render("loading…") + "\n"
	case m.isSelect():
		s += m.renderChoices()
	case m.isFile():
		s += m.renderFile()
	default:
		s += m.ti.View() + "\n"
	}
	if m.err != nil {
		s += theme.Err.Render("  "+m.err.Error()) + "\n"
	}
	return tea.NewView(s)
}

// renderSectionHeader returns a colorful, lightly-decorated header
// line for a wizard section. Format: "━━ Title ━━────────" with the
// accent color on the title and muted rule characters around it.
// Blank line above for breathing room.
func renderSectionHeader(title string) string {
	const ruleLeft = "━━ "
	const ruleRight = " "
	rest := strings.Repeat("─", 48)
	return "\n" + theme.Key.Render(ruleLeft) +
		theme.Heading.Render(title) +
		theme.Key.Render(ruleRight) +
		theme.MutedText.Render(rest)
}

func (m *model) renderFile() string {
	if m.fp == nil {
		return ""
	}
	path := m.fp.Path
	if path != "" {
		return "  " + theme.Key.Render("▸ ") + "selected: " + theme.Value.Render(path) + "\n"
	}
	return "  " + strings.ReplaceAll(m.fp.View(), "\n", "\n  ") + "\n"
}

func (m *model) renderChoices() string {
	idx := m.state.Idx()
	entry := m.state.Entry(idx)
	choices := entry.Choices
	selIdx := entry.SelIdx
	if len(choices) == 0 {
		return theme.MutedText.Render("  (no options available)") + "\n"
	}
	multi := m.isMulti()
	var s string
	// Show column headers if the first choice has Help (used as header by SuggestFrom).
	if choices[0].Help != "" {
		indent := "  "
		if multi {
			// Align header with the checkbox column below.
			indent = "      "
		}
		s += indent + theme.Label.Render(choices[0].Help) + "\n"
	}
	// Cap visible choices to fit the terminal with a scroll window.
	maxVisible := len(choices)
	if m.termH > 0 {
		// Reserve lines for: committed fields, label, header, hint, padding.
		histLen := len(m.state.CommittedHistory())
		maxVisible = m.termH - histLen - 4
		if maxVisible < 5 {
			maxVisible = 5
		}
	}
	start, end := 0, len(choices)
	if len(choices) > maxVisible {
		half := maxVisible / 2
		start = selIdx - half
		if start < 0 {
			start = 0
		}
		end = start + maxVisible
		if end > len(choices) {
			end = len(choices)
			start = end - maxVisible
		}
	}
	if start > 0 {
		s += theme.MutedText.Render(fmt.Sprintf("  ↑ %d more", start)) + "\n"
	}
	for i := start; i < end; i++ {
		c := choices[i]
		line := c.Display
		if line == "" {
			line = c.Value
			if !multi && c.Help != "" {
				line += theme.MutedText.Render("  " + c.Help)
			}
		}
		switch {
		case multi:
			box := "[ ]"
			if entry.MultiSel[i] {
				box = theme.Value.Render("[x]")
			}
			if i == selIdx {
				s += theme.Key.Render("▸") + " " + box + " " + theme.Value.Render(line) + "\n"
			} else {
				s += "  " + box + " " + line + "\n"
			}
		case i == selIdx:
			s += theme.Key.Render("▸") + " " + theme.Value.Render(line) + "\n"
		default:
			s += "  " + line + "\n"
		}
	}
	if end < len(choices) {
		s += theme.MutedText.Render(fmt.Sprintf("  ↓ %d more", len(choices)-end)) + "\n"
	}
	var hint string
	switch {
	case multi:
		n := len(entry.MultiSel)
		hint = fmt.Sprintf("  %d selected · space toggle · ↑/↓ move · enter confirm · esc cancel", n)
	default:
		hint = "  ↑/↓ select · enter confirm · esc cancel"
		if f := m.curField(); f != nil && !f.Required {
			hint = "  ↑/↓ select · enter confirm · tab skip · esc cancel"
		}
	}
	s += theme.MutedText.Render(hint) + "\n"
	return s
}
