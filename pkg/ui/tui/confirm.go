package tui

import (
	"fmt"

	"charm.land/lipgloss/v2"
	"github.com/bwagner5/go-cli-template/pkg/registry"
	"github.com/bwagner5/go-cli-template/pkg/ui/theme"
)

// confirmOverlay is a yes/no modal shown before destructive sagas.
type confirmOverlay struct {
	active   bool
	prompt   string
	yes      bool // cursor: true = Yes selected
	resource *registry.Resource
	op       *registry.Operation
	input    registry.Input
}

func (c *confirmOverlay) Active() bool { return c.active }

func (c *confirmOverlay) Show(prompt string, res *registry.Resource, op *registry.Operation, input registry.Input) {
	c.active = true
	c.prompt = prompt
	c.yes = false
	c.resource = res
	c.op = op
	c.input = input
}

func (c *confirmOverlay) Clear() {
	c.active = false
}

// HandleKey processes a key press. Returns (accepted bool, confirmed bool).
// accepted=true means the overlay consumed the key.
func (c *confirmOverlay) HandleKey(key string) (accepted, confirmed bool) {
	if !c.active {
		return false, false
	}
	switch key {
	case "y", "Y":
		c.active = false
		return true, true
	case "n", "N", "esc":
		c.active = false
		return true, false
	case "left", "h", "tab":
		c.yes = !c.yes
		return true, false
	case "right", "l":
		c.yes = !c.yes
		return true, false
	case "enter":
		c.active = false
		return true, c.yes
	}
	return true, false // consume all other keys while active
}

func (c *confirmOverlay) Box(w, _ int) string {
	width := 50
	if w < width+4 {
		width = w - 4
	}

	prompt := lipgloss.NewStyle().Width(width - 4).Render(c.prompt)

	yesStyle := theme.MutedText
	noStyle := theme.MutedText
	if c.yes {
		yesStyle = lipgloss.NewStyle().Background(theme.Success).Foreground(lipgloss.Color("#0b0f14")).Bold(true).Padding(0, 2)
	} else {
		noStyle = lipgloss.NewStyle().Background(theme.Danger).Foreground(lipgloss.Color("#0b0f14")).Bold(true).Padding(0, 2)
	}

	buttons := fmt.Sprintf("  %s   %s",
		noStyle.Render(" No "),
		yesStyle.Render(" Yes "),
	)

	hint := theme.MutedText.Render("y/n · enter · esc to cancel")

	body := prompt + "\n\n" + buttons + "\n\n" + hint
	return theme.Border.Width(width).Render(body)
}
