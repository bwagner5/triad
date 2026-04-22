package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/bwagner5/triad/pkg/ui/theme"
)

// helpOverlay is the "?" modal listing every binding for the current screen,
// grouped into vertical columns by category (Navigation / Resource / Global).
type helpOverlay struct{}

func newHelp() helpOverlay { return helpOverlay{} }

// Box renders the help modal for the supplied keymap.
func (helpOverlay) Box(w, _ int, bindings []binding) string {
	cats := []string{"Navigation", "Resource", "Global"}
	cols := make([]string, 0, len(cats))
	for _, cat := range cats {
		cols = append(cols, renderHelpColumn(cat, bindings))
	}

	gap := "   "
	body := lipgloss.JoinHorizontal(lipgloss.Top,
		cols[0], gap, cols[1], gap, cols[2],
	)

	width := lipgloss.Width(body) + 4
	if min := 60; width < min {
		width = min
	}
	if max := w - 4; width > max && max > 0 {
		width = max
	}

	var s strings.Builder
	s.WriteString(theme.Heading.Render("Help"))
	s.WriteString("\n\n")
	s.WriteString(body)
	s.WriteString("\n\n")
	s.WriteString(theme.MutedText.Render("press ? or esc to close"))

	return theme.Border.Width(width).Render(s.String())
}

// renderHelpColumn returns a single category column: heading + one row per
// binding (<key>  label).
func renderHelpColumn(cat string, bindings []binding) string {
	var rows []string
	rows = append(rows, theme.Label.Render(cat))
	// Compute max key width so labels align.
	maxKey := 0
	for _, b := range bindings {
		if b.Cat == cat && len(b.Key) > maxKey {
			maxKey = len(b.Key)
		}
	}
	for _, b := range bindings {
		if b.Cat != cat {
			continue
		}
		pad := strings.Repeat(" ", maxKey-len(b.Key))
		rows = append(rows,
			theme.Key.Render("<"+b.Key+">")+pad+"  "+b.Label,
		)
	}
	return strings.Join(rows, "\n")
}
