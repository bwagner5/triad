// Package wizard is an inline (non-alt-screen) bubbletea v2 flow that
// prompts for missing required Fields. Populated answers are written back
// into the provided registry.Input.
//
// Fields with a Suggest function render as a single-selection list; fields
// without one render as a text input.
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
	return nil
}

type model struct {
	ctx    context.Context
	fields []registry.Field
	in     registry.Input
	idx    int
	reason string // optional header printed once above the first prompt

	ti                textinput.Model
	fp                *filepicker.Model // non-nil when current field is File
	spin              spinner.Model
	loading           bool
	choices           []registry.Choice
	selIdx            int          // cursor for selection list
	multiSel          map[int]bool // checked choice indices for the current Multi field
	committed         []string     // rendered lines for previously-answered fields
	committedSections []string     // section name for each committed line (parallel)
	committedIdx      []int        // field index for each committed line (for goBack)
	termH             int      // terminal height for scroll capping

	err      error
	canceled bool
}

func newModel(ctx context.Context, fields []registry.Field, in registry.Input) *model {
	ti := textinput.New()
	ti.Prompt = "› "
	sp := spinner.New()
	return &model{ctx: ctx, fields: fields, in: in, ti: ti, spin: sp}
}

// curField returns the field we're currently collecting.
func (m *model) curField() *registry.Field {
	if m.idx >= len(m.fields) {
		return nil
	}
	return &m.fields[m.idx]
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
func (m *model) startField() tea.Cmd {
	for m.idx < len(m.fields) {
		f := &m.fields[m.idx]
		// Skip fields whose When predicate returns false. Also clear
		// any stale value so it doesn't leak into the saga.
		if f.When != nil && !f.When(m.in) {
			delete(m.in, f.Flag)
			m.idx++
			continue
		}
		// If the field already has a value (from a prior pass before
		// goBack), auto-recommit it and advance. This preserves
		// downstream answers the user already gave.
		if v, ok := m.in[f.Flag]; ok && v != "" {
			labelText := f.DisplayLabel()
			displayVal := v
			if f.Sensitive {
				displayVal = strings.Repeat("•", len(v))
			}
			m.committed = append(m.committed,
				fmt.Sprintf("%s %s: %s", theme.OKMark, theme.Label.Render(labelText), displayVal))
			m.committedSections = append(m.committedSections, f.Section)
			m.committedIdx = append(m.committedIdx, m.idx)
			m.idx++
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
	m.selIdx = 0
	m.multiSel = nil
	m.choices = nil
	m.err = nil
	if m.isSelect() {
		// Selection mode: hide text input, fetch choices with spinner.
		m.ti.Blur()
		m.loading = true
		return m.fetchSuggest(*f)
	}
	if m.isFile() {
		// File picker mode.
		fp := filepicker.New()
		fp.AllowedTypes = f.AllowedExts
		fp.ShowHidden = false
		fp.AutoHeight = false
		fp.SetHeight(10)
		if cwd, err := os.Getwd(); err == nil {
			fp.CurrentDirectory = cwd
		}
		m.fp = &fp
		m.ti.Blur()
		m.loading = false
		return fp.Init()
	}
	// Text input mode.
	m.loading = false
	m.ti.Placeholder = f.Help
	// Seed with 3-tier precedence: Input > Prefill > Default.
	// Matches the TUI wizard so both UIs behave identically.
	var seed string
	if v, ok := m.in[f.Flag]; ok && v != "" {
		seed = v
	} else if f.Prefill != nil {
		seed = f.Prefill()
	} else if f.Default != nil {
		seed = fmt.Sprintf("%v", f.Default)
	}
	if seed != "" {
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
	idx := m.idx
	return func() tea.Msg {
		cs, err := f.Suggest(m.ctx)
		return suggestMsg{idx: idx, choices: cs, err: err}
	}
}

// commitAndAdvance records val for the current field and moves to the next.
func (m *model) commitAndAdvance(val string) tea.Cmd {
	f := m.curField()
	if f == nil {
		return tea.Quit
	}
	if err := f.ValidateValue(val); err != nil {
		m.err = err
		return nil
	}
	m.in[f.Flag] = val
	labelText := f.DisplayLabel()
	displayVal := val
	if f.Sensitive {
		displayVal = strings.Repeat("•", len(val))
	}
	m.committed = append(m.committed,
		fmt.Sprintf("%s %s: %s", theme.OKMark, theme.Label.Render(labelText), displayVal))
	m.committedSections = append(m.committedSections, f.Section)
	// Track which field index produced each committed line so goBack
	// can find the right field to return to.
	m.committedIdx = append(m.committedIdx, m.idx)
	m.idx++
	return m.startField()
}

// goBack returns to the previous answered field. Removes committed
// history from that point onward (startField will re-add still-valid
// answers automatically) and clears only the target field's value so
// the user can change it. Downstream answers are preserved unless
// their When predicate becomes false after the change.
func (m *model) goBack() tea.Cmd {
	if len(m.committedIdx) == 0 {
		return nil // nothing to go back to
	}
	// Find the field to return to.
	prevIdx := m.committedIdx[len(m.committedIdx)-1]

	// Truncate committed history to just before that field. Any
	// entries after it will be re-added by startField if their
	// values are still in m.in and their When still holds.
	// Find how many committed entries to keep (all before prevIdx).
	keepN := len(m.committedIdx) - 1
	for keepN > 0 && m.committedIdx[keepN-1] >= prevIdx {
		keepN--
	}
	m.committed = m.committed[:keepN]
	m.committedSections = m.committedSections[:keepN]
	m.committedIdx = m.committedIdx[:keepN]

	// Clear only the target field so it gets re-prompted.
	delete(m.in, m.fields[prevIdx].Flag)
	m.idx = prevIdx
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
				// Commit the comma-joined list of checked choices in
				// choice order. Empty selection is allowed only for
				// non-required fields; required fields loop back and
				// show the required error.
				var picks []string
				for idx, c := range m.choices {
					if m.multiSel[idx] {
						picks = append(picks, c.Value)
					}
				}
				return m, m.commitAndAdvance(strings.Join(picks, ","))
			}
			if m.isSelect() {
				if m.loading || len(m.choices) == 0 {
					return m, nil
				}
				return m, m.commitAndAdvance(m.choices[m.selIdx].Value)
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
			if m.isMulti() && !m.loading && len(m.choices) > 0 {
				if m.multiSel == nil {
					m.multiSel = map[int]bool{}
				}
				if m.multiSel[m.selIdx] {
					delete(m.multiSel, m.selIdx)
				} else {
					m.multiSel[m.selIdx] = true
				}
				return m, nil
			}
		case "up", "k":
			if m.isSelect() && m.selIdx > 0 {
				m.selIdx--
				return m, nil
			}
		case "down", "j":
			if m.isSelect() && m.selIdx < len(m.choices)-1 {
				m.selIdx++
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
		if msg.idx != m.idx {
			return m, nil
		}
		m.loading = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.choices = msg.choices
		// Seed cursor to the saved answer if the current field's
		// Value matches one of the loaded choices. Matches the
		// text-input seeding (Input > Prefill > Default) so a
		// resumed wizard starts with the prior pick highlighted.
		if f := m.curField(); f != nil {
			want := m.in[f.Flag]
			if want == "" && f.Prefill != nil {
				want = f.Prefill()
			}
			if want == "" && f.Default != nil {
				want = fmt.Sprintf("%v", f.Default)
			}
			if want != "" {
				if m.isMulti() {
					// Comma-split the seed and pre-check matching choices
					// so a resumed wizard shows the previous picks.
					set := map[string]bool{}
					for _, v := range strings.Split(want, ",") {
						v = strings.TrimSpace(v)
						if v != "" {
							set[v] = true
						}
					}
					sel := map[int]bool{}
					for i, c := range m.choices {
						if set[c.Value] {
							sel[i] = true
						}
					}
					if len(sel) > 0 {
						m.multiSel = sel
					}
				} else {
					for i, c := range m.choices {
						if c.Value == want {
							m.selIdx = i
							break
						}
					}
				}
			}
		}
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
	// consecutive entries come from different sections. Entries with
	// an empty section are treated as "no section" and don't print a
	// header.
	prevSection := ""
	for i, c := range m.committed {
		sec := ""
		if i < len(m.committedSections) {
			sec = m.committedSections[i]
		}
		if sec != "" && sec != prevSection {
			s += renderSectionHeader(sec) + "\n"
		}
		prevSection = sec
		s += c + "\n"
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
		preamble = f.PreambleFunc(m.in)
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

// renderSectionHeader returns a colourful, lightly-decorated header
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
	if len(m.choices) == 0 {
		return theme.MutedText.Render("  (no options available)") + "\n"
	}
	multi := m.isMulti()
	var s string
	// Show column headers if the first choice has Help (used as header by SuggestFrom).
	if m.choices[0].Help != "" {
		indent := "  "
		if multi {
			// Align header with the checkbox column below.
			indent = "      "
		}
		s += indent + theme.Label.Render(m.choices[0].Help) + "\n"
	}
	// Cap visible choices to fit the terminal with a scroll window.
	maxVisible := len(m.choices)
	if m.termH > 0 {
		// Reserve lines for: committed fields, label, header, hint, padding.
		maxVisible = m.termH - len(m.committed) - 4
		if maxVisible < 5 {
			maxVisible = 5
		}
	}
	start, end := 0, len(m.choices)
	if len(m.choices) > maxVisible {
		half := maxVisible / 2
		start = m.selIdx - half
		if start < 0 {
			start = 0
		}
		end = start + maxVisible
		if end > len(m.choices) {
			end = len(m.choices)
			start = end - maxVisible
		}
	}
	if start > 0 {
		s += theme.MutedText.Render(fmt.Sprintf("  ↑ %d more", start)) + "\n"
	}
	for i := start; i < end; i++ {
		c := m.choices[i]
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
			if m.multiSel[i] {
				box = theme.Value.Render("[x]")
			}
			if i == m.selIdx {
				s += theme.Key.Render("▸") + " " + box + " " + theme.Value.Render(line) + "\n"
			} else {
				s += "  " + box + " " + line + "\n"
			}
		case i == m.selIdx:
			s += theme.Key.Render("▸") + " " + theme.Value.Render(line) + "\n"
		default:
			s += "  " + line + "\n"
		}
	}
	if end < len(m.choices) {
		s += theme.MutedText.Render(fmt.Sprintf("  ↓ %d more", len(m.choices)-end)) + "\n"
	}
	var hint string
	switch {
	case multi:
		n := len(m.multiSel)
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
