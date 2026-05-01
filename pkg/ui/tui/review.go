package tui

import (
	"fmt"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/bwagner5/triad/pkg/registry"
	"github.com/bwagner5/triad/pkg/ui/theme"
)

// reviewOverlay is a scrollable full-screen overlay for reviewing long
// content (e.g. IAM policies) with a yes/no confirmation at the bottom.
// It wraps the bubbles viewport for native pgup/pgdown/arrow scrolling.
type reviewOverlay struct {
	active   bool
	vp       viewport.Model
	yes      bool // cursor: true = Yes selected
	field    registry.Field
	op       *registry.Operation
	resource *registry.Resource
	input    registry.Input
	w, h     int
}

func (r *reviewOverlay) Active() bool     { return r.active }
func (r *reviewOverlay) SetSize(w, h int) { r.w = w; r.h = h }

// Show opens the review overlay with scrollable content and a yes/no
// field. The field's value is written into input on confirmation.
func (r *reviewOverlay) Show(content string, field registry.Field, op *registry.Operation, res *registry.Resource, input registry.Input) {
	r.active = true
	r.field = field
	r.op = op
	r.resource = res
	r.input = input
	// Default to Yes (the user already opted in at the prior prompt).
	r.yes = true

	vpW := r.w - 4
	if vpW < 40 {
		vpW = 40
	}
	// Reserve: border(2) + heading(2) + buttons(3) + hint(2) + scroll%(1)
	vpH := r.h - 10
	if vpH < 5 {
		vpH = 5
	}
	r.vp = viewport.New(viewport.WithWidth(vpW), viewport.WithHeight(vpH))
	r.vp.SoftWrap = true
	r.vp.SetContent(content)
}

func (r *reviewOverlay) Clear() {
	r.active = false
}

func (r *reviewOverlay) Update(msg tea.Msg) (bool, tea.Cmd) {
	if !r.active {
		return false, nil
	}
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "y", "Y":
			return true, r.finish(true)
		case "n", "N", "esc":
			return true, r.finish(false)
		case "left", "right", "h", "l", "tab":
			r.yes = !r.yes
			return true, nil
		case "enter":
			return true, r.finish(r.yes)
		default:
			// Forward to viewport for scroll handling.
			var cmd tea.Cmd
			r.vp, cmd = r.vp.Update(msg)
			return true, cmd
		}
	default:
		var cmd tea.Cmd
		r.vp, cmd = r.vp.Update(msg)
		return true, cmd
	}
}

func (r *reviewOverlay) finish(confirmed bool) tea.Cmd {
	r.active = false
	if !confirmed {
		r.input[r.field.Flag] = "false"
	} else {
		r.input[r.field.Flag] = "true"
	}
	return func() tea.Msg {
		return wizardDoneMsg{resource: r.resource, op: r.op, input: r.input}
	}
}

func (r *reviewOverlay) Box(w, h int) string {
	boxW := w - 4
	if boxW < 40 {
		boxW = 40
	}

	// Resize viewport if terminal changed.
	vpH := h - 10
	if vpH < 5 {
		vpH = 5
	}
	r.vp.SetWidth(boxW - 2)
	r.vp.SetHeight(vpH)

	header := theme.Heading.Render("Review") + "\n\n"

	scrollPct := fmt.Sprintf(" %d%% ", int(r.vp.ScrollPercent()*100))
	scrollInfo := theme.MutedText.Render(scrollPct)

	yesStyle := theme.MutedText
	noStyle := theme.MutedText
	if r.yes {
		yesStyle = theme.Value
	} else {
		noStyle = theme.Value
	}
	buttons := fmt.Sprintf("\n  %s   %s\n",
		noStyle.Render("[ No ]"),
		yesStyle.Render("[ Yes ]"),
	)
	hint := theme.MutedText.Render("  ↑/↓/pgup/pgdown scroll · y/n · enter confirm · esc cancel")

	body := header + r.vp.View() + "\n" + scrollInfo + buttons + "\n" + hint
	return theme.Border.Width(boxW).MaxWidth(boxW + 2).Render(body)
}
