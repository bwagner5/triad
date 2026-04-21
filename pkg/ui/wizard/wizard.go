// Package wizard is an inline (non-alt-screen) bubbletea v2 flow that
// prompts for missing required Fields. Populated answers are written back
// into the provided registry.Input.
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
	committed []string // rendered lines for previously-answered fields

	err      error
	canceled bool
}

func newModel(ctx context.Context, fields []registry.Field, in registry.Input) *model {
	ti := textinput.New()
	ti.Prompt = "› "
	sp := spinner.New()
	m := &model{ctx: ctx, fields: fields, in: in, ti: ti, spin: sp}
	return m
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(m.startField(), m.spin.Tick)
}

// startField focuses the input and, if Suggest is set, kicks off a loader.
func (m *model) startField() tea.Cmd {
	if m.idx >= len(m.fields) {
		return tea.Quit
	}
	f := m.fields[m.idx]
	m.ti.Reset()
	m.ti.Placeholder = f.Help
	if f.Sensitive {
		m.ti.EchoMode = textinput.EchoPassword
	} else {
		m.ti.EchoMode = textinput.EchoNormal
	}
	focus := m.ti.Focus()
	if f.Suggest != nil {
		m.loading = true
		return tea.Batch(focus, m.fetchSuggest(f))
	}
	m.loading = false
	return focus
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

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			m.canceled = true
			return m, tea.Quit
		case "enter":
			if m.idx >= len(m.fields) {
				return m, tea.Quit
			}
			f := m.fields[m.idx]
			val := m.ti.Value()
			if val == "" && len(m.choices) > 0 {
				val = m.choices[0].Value
			}
			if val == "" && f.Required {
				return m, nil
			}
			if f.Validate != nil {
				if err := f.Validate(val); err != nil {
					m.err = err
					return m, nil
				}
			}
			m.in[f.Flag] = val
			m.committed = append(m.committed,
				fmt.Sprintf("%s %s: %s", theme.OKMark, theme.Label.Render(f.Flag), val))
			m.idx++
			m.err = nil
			m.choices = nil
			return m, m.startField()
		case "tab":
			if len(m.choices) > 0 {
				m.ti.SetValue(m.choices[0].Value)
			}
			return m, nil
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
		if sug := make([]string, 0, len(msg.choices)); true {
			for _, c := range msg.choices {
				sug = append(sug, c.Value)
			}
			m.ti.SetSuggestions(sug)
			m.ti.ShowSuggestions = true
		}
		return m, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	}
	var cmd tea.Cmd
	m.ti, cmd = m.ti.Update(msg)
	return m, cmd
}

func (m *model) View() tea.View {
	var s string
	for _, c := range m.committed {
		s += c + "\n"
	}
	if m.idx >= len(m.fields) {
		return tea.NewView(s)
	}
	f := m.fields[m.idx]
	label := theme.Label.Render(f.Flag)
	if f.Required {
		label += theme.Err.Render(" *")
	}
	help := ""
	if f.Help != "" {
		help = theme.MutedText.Render("  " + f.Help)
	}
	s += label + help + "\n"
	if m.loading {
		s += m.spin.View() + " " + theme.MutedText.Render("loading suggestions…") + "\n"
	} else if len(m.choices) > 0 {
		s += theme.MutedText.Render("  suggestions: ")
		for i, c := range m.choices {
			if i > 0 {
				s += theme.MutedText.Render(", ")
			}
			s += c.Value
			if i >= 4 {
				s += theme.MutedText.Render(", …")
				break
			}
		}
		s += "\n"
	}
	s += m.ti.View() + "\n"
	if m.err != nil {
		s += theme.Err.Render("  "+m.err.Error()) + "\n"
	}
	return tea.NewView(s)
}
