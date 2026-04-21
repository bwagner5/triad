package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	"github.com/bwagner5/go-cli-template/pkg/registry"
	"github.com/bwagner5/go-cli-template/pkg/ui/theme"
)

// wizardOverlay is a full-screen form overlay that collects saga fields
// one at a time, replacing the tea.Exec inline wizard approach.
type wizardOverlay struct {
	active   bool
	ctx      context.Context
	resource *registry.Resource
	saga     *registry.Saga
	fields   []registry.Field
	input    registry.Input
	idx      int // current field index

	ti      textinput.Model
	spin    spinner.Model
	loading bool
	choices []registry.Choice
	selIdx  int

	committed []committedField // already-answered fields
	err       error
	w, h      int
}

type committedField struct {
	flag string
	val  string
}

func newWizardOverlay() wizardOverlay {
	ti := textinput.New()
	ti.Prompt = "› "
	sp := spinner.New()
	return wizardOverlay{ti: ti, spin: sp}
}

func (wo *wizardOverlay) Active() bool     { return wo.active }
func (wo *wizardOverlay) SetSize(w, h int) { wo.w, wo.h = w, h }

func (wo *wizardOverlay) Show(ctx context.Context, res *registry.Resource, saga *registry.Saga, fields []registry.Field, input registry.Input) tea.Cmd {
	wo.active = true
	wo.ctx = ctx
	wo.resource = res
	wo.saga = saga
	wo.fields = fields
	wo.input = input
	wo.idx = 0
	wo.committed = nil
	wo.err = nil
	return tea.Batch(wo.startField(), wo.spin.Tick)
}

func (wo *wizardOverlay) Clear() {
	wo.active = false
	wo.fields = nil
	wo.committed = nil
	wo.choices = nil
	wo.err = nil
}

func (wo *wizardOverlay) curField() *registry.Field {
	if wo.idx >= len(wo.fields) {
		return nil
	}
	return &wo.fields[wo.idx]
}

func (wo *wizardOverlay) isSelect() bool {
	f := wo.curField()
	return f != nil && f.Suggest != nil
}

func (wo *wizardOverlay) startField() tea.Cmd {
	f := wo.curField()
	if f == nil {
		return nil
	}
	wo.ti.Reset()
	wo.selIdx = 0
	wo.choices = nil
	wo.err = nil
	if f.Suggest != nil {
		wo.ti.Blur()
		wo.loading = true
		return wo.fetchSuggest(*f)
	}
	wo.loading = false
	wo.ti.Placeholder = f.Help
	if f.Sensitive {
		wo.ti.EchoMode = textinput.EchoPassword
	} else {
		wo.ti.EchoMode = textinput.EchoNormal
	}
	return wo.ti.Focus()
}

type wizardSuggestMsg struct {
	idx     int
	choices []registry.Choice
	err     error
}

func (wo *wizardOverlay) fetchSuggest(f registry.Field) tea.Cmd {
	idx := wo.idx
	ctx := wo.ctx
	return func() tea.Msg {
		cs, err := f.Suggest(ctx)
		return wizardSuggestMsg{idx: idx, choices: cs, err: err}
	}
}

func (wo *wizardOverlay) commitAndAdvance(val string) tea.Cmd {
	f := wo.curField()
	if f == nil {
		return nil
	}
	if f.Validate != nil {
		if err := f.Validate(val); err != nil {
			wo.err = err
			return nil
		}
	}
	wo.input[f.Flag] = val
	wo.committed = append(wo.committed, committedField{flag: f.Flag, val: val})
	wo.idx++
	if wo.curField() == nil {
		// All fields collected — signal completion.
		wo.active = false
		return func() tea.Msg {
			return wizardDoneMsg{
				resource: wo.resource,
				saga:     wo.saga,
				input:    wo.input,
			}
		}
	}
	return wo.startField()
}

// wizardDoneMsg is posted when the wizard overlay finishes collecting all fields.
type wizardDoneMsg struct {
	resource *registry.Resource
	saga     *registry.Saga
	input    registry.Input
}

// Update processes messages for the wizard overlay. Returns (consumed, cmd).
func (wo *wizardOverlay) Update(msg tea.Msg) (bool, tea.Cmd) {
	if !wo.active {
		return false, nil
	}
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return true, wo.handleKey(msg)
	case wizardSuggestMsg:
		if msg.idx != wo.idx {
			return true, nil
		}
		wo.loading = false
		if msg.err != nil {
			wo.err = msg.err
		} else {
			wo.choices = msg.choices
		}
		return true, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		wo.spin, cmd = wo.spin.Update(msg)
		return true, cmd
	}
	// Forward to text input if active.
	if !wo.isSelect() && wo.active {
		var cmd tea.Cmd
		wo.ti, cmd = wo.ti.Update(msg)
		return true, cmd
	}
	return true, nil
}

func (wo *wizardOverlay) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	key := msg.String()
	switch key {
	case "ctrl+c", "esc":
		wo.Clear()
		return nil
	case "enter":
		if wo.curField() == nil {
			return nil
		}
		if wo.isSelect() {
			if wo.loading || len(wo.choices) == 0 {
				return nil
			}
			return wo.commitAndAdvance(wo.choices[wo.selIdx].Value)
		}
		val := wo.ti.Value()
		if val == "" && wo.curField().Required {
			return nil
		}
		return wo.commitAndAdvance(val)
	case "up", "k":
		if wo.isSelect() && wo.selIdx > 0 {
			wo.selIdx--
		}
		return nil
	case "down", "j":
		if wo.isSelect() && wo.selIdx < len(wo.choices)-1 {
			wo.selIdx++
		}
		return nil
	}
	// Forward to text input.
	if !wo.isSelect() {
		var cmd tea.Cmd
		wo.ti, cmd = wo.ti.Update(msg)
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

	sagaName := ""
	if wo.saga != nil {
		sagaName = wo.saga.Name
	}
	header := theme.Heading.Render(sagaName)

	// Progress indicator.
	total := len(wo.fields)
	done := wo.idx
	progress := theme.MutedText.Render(fmt.Sprintf("  (%d/%d)", done, total))

	var body strings.Builder
	body.WriteString(header + progress + "\n\n")

	// Show committed fields.
	for _, c := range wo.committed {
		body.WriteString(fmt.Sprintf("  %s %s: %s\n",
			theme.OKMark,
			theme.Label.Render(c.flag),
			c.val,
		))
	}

	// Current field.
	f := wo.curField()
	if f != nil {
		label := theme.Value.Render(f.Flag)
		if f.Required {
			label += theme.Err.Render(" *")
		}
		if f.Help != "" {
			label += theme.MutedText.Render("  " + f.Help)
		}
		body.WriteString("\n  " + label + "\n")

		switch {
		case wo.isSelect() && wo.loading:
			body.WriteString("  " + wo.spin.View() + " " + theme.MutedText.Render("loading…") + "\n")
		case wo.isSelect():
			body.WriteString(wo.renderChoices())
		default:
			body.WriteString("  " + wo.ti.View() + "\n")
		}

		if wo.err != nil {
			body.WriteString("  " + theme.Err.Render(wo.err.Error()) + "\n")
		}
	}

	body.WriteString("\n" + theme.MutedText.Render("  enter to confirm · esc to cancel"))

	content := body.String()
	return theme.Border.Width(width).Render(content)
}

func (wo *wizardOverlay) renderChoices() string {
	if len(wo.choices) == 0 {
		return "  " + theme.MutedText.Render("(no options available)") + "\n"
	}
	var s strings.Builder
	for i, c := range wo.choices {
		marker := "    "
		line := c.Value
		if c.Help != "" {
			line += theme.MutedText.Render("  " + c.Help)
		}
		if i == wo.selIdx {
			marker = "  " + theme.Key.Render("▸ ")
			line = theme.Value.Render(line)
		}
		s.WriteString(marker + line + "\n")
	}
	s.WriteString("  " + theme.MutedText.Render("↑/↓ select · enter confirm") + "\n")
	return s.String()
}
