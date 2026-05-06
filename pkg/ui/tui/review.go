package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/bwagner5/triad/pkg/registry"
	"github.com/bwagner5/triad/pkg/ui/theme"
)

// reviewOverlay is a scrollable overlay for reviewing content (e.g. IAM
// policies) with a yes/no confirmation at the bottom. It wraps the
// bubbles viewport for native pgup/pgdown/arrow scrolling. Width adapts
// to content: short prompts get a compact box, long content expands.
type reviewOverlay struct {
	active   bool
	vp       viewport.Model
	yes      bool // cursor: true = Yes selected
	field    registry.Field
	op       *registry.Operation
	resource *registry.Resource
	input    registry.Input
	w, h     int
	contentW int // widest content line (drives dynamic box width)
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
	r.yes = true

	// Measure widest line to drive dynamic box width.
	maxLine := 0
	for _, line := range strings.Split(content, "\n") {
		if n := len(line); n > maxLine {
			maxLine = n
		}
	}
	r.contentW = maxLine

	boxW := r.boxWidth(r.w)
	vpW := boxW - 2

	vpH := r.h - 10
	if vpH < 5 {
		vpH = 5
	}
	contentLines := strings.Count(content, "\n") + 1
	if contentLines < vpH {
		vpH = contentLines
	}
	r.vp = viewport.New(viewport.WithWidth(vpW), viewport.WithHeight(vpH))
	r.vp.SoftWrap = true
	r.vp.SetContent(content)
}

// boxWidth computes the overlay width: max(60, content needs + padding)
// capped at terminal width - 4.
func (r *reviewOverlay) boxWidth(termW int) int {
	// content + border padding (4 chars for border + inner margin)
	w := r.contentW + 4
	if w < 60 {
		w = 60
	}
	if w > termW-4 {
		w = termW - 4
	}
	if w < 40 {
		w = 40
	}
	return w
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
	boxW := r.boxWidth(w)

	vpH := h - 10
	if vpH < 5 {
		vpH = 5
	}
	totalLines := r.vp.TotalLineCount()
	if totalLines > 0 && totalLines < vpH {
		vpH = totalLines
	}
	r.vp.SetWidth(boxW - 2)
	r.vp.SetHeight(vpH)

	header := theme.Heading.Render("Review") + "\n\n"

	// Only show scroll percentage when content overflows the viewport.
	scrollInfo := ""
	if totalLines > vpH {
		scrollInfo = theme.MutedText.Render(fmt.Sprintf(" %d%% ", int(r.vp.ScrollPercent()*100)))
	}

	yesStyle := theme.MutedText
	noStyle := theme.MutedText
	if r.yes {
		yesStyle = lipgloss.NewStyle().Background(theme.Success).Foreground(lipgloss.Color("#0b0f14")).Bold(true).Padding(0, 2)
	} else {
		noStyle = lipgloss.NewStyle().Background(theme.Danger).Foreground(lipgloss.Color("#0b0f14")).Bold(true).Padding(0, 2)
	}
	buttons := fmt.Sprintf("\n  %s   %s\n",
		noStyle.Render(" No "),
		yesStyle.Render(" Yes "),
	)
	hint := theme.MutedText.Render("  ↑/↓ scroll · y/n · enter confirm · esc cancel")

	body := header + r.vp.View() + "\n" + scrollInfo + buttons + "\n" + hint
	return theme.Border.Width(boxW).MaxWidth(boxW + 2).Render(body)
}
