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

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	"github.com/bwagner5/go-cli-template/pkg/registry"
	"github.com/bwagner5/go-cli-template/pkg/ui/theme"
)

// Collect prompts for each Field and writes answers into in.
// Runs inline (no alt-screen) so output blends with the surrounding CLI.
func Collect(ctx context.Context, fields []registry.Field, in registry.Input) error {
	if len(fields) == 0 {
		return nil
	}
	m := newModel(ctx, fields, in)
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

	ti        textinput.Model
	spin      spinner.Model
	loading   bool
	choices   []registry.Choice
	selIdx    int      // cursor for selection list
	committed []string // rendered lines for previously-answered fields

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

func (m *model) Init() tea.Cmd {
	return tea.Batch(m.startField(), m.spin.Tick)
}

// startField focuses the input and, if Suggest is set, kicks off a loader.
func (m *model) startField() tea.Cmd {
	f := m.curField()
	if f == nil {
		return tea.Quit
	}
	m.ti.Reset()
	m.selIdx = 0
	m.choices = nil
	m.err = nil
	if f.Suggest != nil {
		// Selection mode: hide text input, fetch choices with spinner.
		m.ti.Blur()
		m.loading = true
		return m.fetchSuggest(*f)
	}
	// Text input mode.
	m.loading = false
	m.ti.Placeholder = f.Help
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
	if f.Validate != nil {
		if err := f.Validate(val); err != nil {
			m.err = err
			return nil
		}
	}
	m.in[f.Flag] = val
	m.committed = append(m.committed,
		fmt.Sprintf("%s %s: %s", theme.OKMark, theme.Label.Render(f.Flag), val))
	m.idx++
	return m.startField()
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			m.canceled = true
			return m, tea.Quit
		case "enter":
			if m.curField() == nil {
				return m, tea.Quit
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
		return m, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	}
	if !m.isSelect() {
		var cmd tea.Cmd
		m.ti, cmd = m.ti.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *model) View() tea.View {
	var s string
	for _, c := range m.committed {
		s += c + "\n"
	}
	f := m.curField()
	if f == nil {
		return tea.NewView(s)
	}
	label := theme.Label.Render(f.Flag)
	if f.Required {
		label += theme.Err.Render(" *")
	}
	if f.Help != "" {
		label += theme.MutedText.Render("  " + f.Help)
	}
	s += label + "\n"

	switch {
	case m.isSelect() && m.loading:
		s += m.spin.View() + " " + theme.MutedText.Render("loading…") + "\n"
	case m.isSelect():
		s += m.renderChoices()
	default:
		s += m.ti.View() + "\n"
	}
	if m.err != nil {
		s += theme.Err.Render("  "+m.err.Error()) + "\n"
	}
	return tea.NewView(s)
}

func (m *model) renderChoices() string {
	if len(m.choices) == 0 {
		return theme.MutedText.Render("  (no options available)") + "\n"
	}
	var s string
	for i, c := range m.choices {
		marker := "  "
		line := c.Value
		if c.Help != "" {
			line += theme.MutedText.Render("  "+c.Help)
		}
		if i == m.selIdx {
			marker = theme.Key.Render("▸ ")
			line = theme.Value.Render(line)
		}
		s += marker + line + "\n"
	}
	s += theme.MutedText.Render("  ↑/↓ select · enter confirm · esc cancel") + "\n"
	return s
}
