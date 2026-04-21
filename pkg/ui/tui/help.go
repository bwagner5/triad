package tui

import (
	"charm.land/lipgloss/v2"
	"github.com/bwagner5/go-cli-template/pkg/ui/theme"
)

// helpOverlay is the "?" modal with all key bindings for the current screen.
type helpOverlay struct{}

func newHelp() helpOverlay { return helpOverlay{} }

func (helpOverlay) Layer(w, h int) *lipgloss.Layer {
	sections := [][2]string{
		{"Navigation", "j/k or ↑/↓ move · enter open detail · esc back"},
		{"Actions", "r refresh · : command palette"},
		{"Global", "? toggle this help · q quit"},
	}
	var s string
	s += theme.Heading.Render("Help") + "\n\n"
	for _, sec := range sections {
		s += theme.Label.Render(sec[0]) + "\n"
		s += "  " + sec[1] + "\n\n"
	}
	s += theme.MutedText.Render("press ? or esc to close")
	width := 60
	if w < width+4 {
		width = w - 4
	}
	box := theme.Border.Width(width).Render(s)
	x := (w - lipgloss.Width(box)) / 2
	y := (h - lipgloss.Height(box)) / 2
	return lipgloss.NewLayer(box).X(x).Y(y).Z(3)
}
