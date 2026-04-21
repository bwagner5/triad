package tui

import (
	"github.com/bwagner5/go-cli-template/pkg/ui/theme"
)

// helpOverlay is the "?" modal with all key bindings for the current screen.
type helpOverlay struct{}

func newHelp() helpOverlay { return helpOverlay{} }

func (helpOverlay) Box(w, _ int) string {
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
	return theme.Border.Width(width).Render(s)
}
