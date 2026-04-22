package tui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/textinput"
	"github.com/bwagner5/go-cli-template/pkg/registry"
	"github.com/bwagner5/go-cli-template/pkg/ui/theme"
	"github.com/sahilm/fuzzy"
)

// paletteKind distinguishes navigation targets from executable verbs.
type paletteKind int

const (
	paletteNav paletteKind = iota
)

type paletteEntry struct {
	kind     paletteKind
	display  string
	short    string
	resource *registry.Resource
}

type paletteResultMsg struct {
	entry    *paletteEntry
	canceled bool
}

type paletteModel struct {
	ti      textinput.Model
	entries []paletteEntry
	matches []int // indices into entries after filtering
	cursor  int
	w, h    int
}

func newPalette(reg *registry.Registry) paletteModel {
	var entries []paletteEntry
	for _, res := range reg.All() {
		r := res
		entries = append(entries, paletteEntry{kind: paletteNav, display: r.Plural, short: r.Short, resource: &r})
	}
	ti := textinput.New()
	ti.Prompt = ": "
	ti.Placeholder = "switch resource…"
	m := paletteModel{ti: ti, entries: entries}
	m.filter("")
	return m
}

func (m *paletteModel) SetSize(w, h int) { m.w, m.h = w, h }
func (m *paletteModel) Focus()           { m.ti.Focus(); m.ti.SetValue(""); m.filter("") }

func (m *paletteModel) filter(q string) {
	m.matches = m.matches[:0]
	if q == "" {
		for i := range m.entries {
			m.matches = append(m.matches, i)
		}
		m.cursor = 0
		return
	}
	targets := make([]string, len(m.entries))
	for i, e := range m.entries {
		targets[i] = e.display
	}
	for _, r := range fuzzy.Find(q, targets) {
		m.matches = append(m.matches, r.Index)
	}
	m.cursor = 0
}

func (m paletteModel) Update(msg tea.Msg) (paletteModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc":
			return m, func() tea.Msg { return paletteResultMsg{canceled: true} }
		case "enter":
			if m.cursor < len(m.matches) {
				e := m.entries[m.matches[m.cursor]]
				return m, func() tea.Msg { return paletteResultMsg{entry: &e} }
			}
			return m, func() tea.Msg { return paletteResultMsg{canceled: true} }
		case "down", "ctrl+n":
			if m.cursor < len(m.matches)-1 {
				m.cursor++
			}
			return m, nil
		case "up", "ctrl+p":
			if m.cursor > 0 {
				m.cursor--
			}
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.ti, cmd = m.ti.Update(msg)
	m.filter(m.ti.Value())
	return m, cmd
}

func (m paletteModel) Box(w, _ int) string {
	width := w * 2 / 3
	if width < 40 {
		width = 40
	}
	rows := ""
	maxRows := 10
	for i, mi := range m.matches {
		if i >= maxRows {
			break
		}
		e := m.entries[mi]
		prefix := "  "
		if i == m.cursor {
			prefix = theme.Key.Render("▸ ")
		}
		tag := ""
		rows += fmt.Sprintf("%s%s %s  %s\n", prefix, tag, e.display, theme.MutedText.Render(e.short))
	}
	return theme.Border.Width(width).Render(m.ti.View() + "\n" + rows)
}
