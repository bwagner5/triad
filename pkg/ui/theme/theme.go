// Package theme defines shared lipgloss v2 styles used by every UI.
package theme

import "charm.land/lipgloss/v2"

var (
	Accent  = lipgloss.Color("#7D56F4")
	Success = lipgloss.Color("#04B575")
	Warning = lipgloss.Color("#F2C14E")
	Danger  = lipgloss.Color("#EF4444")
	Muted   = lipgloss.Color("#6B7280")
	Subtle  = lipgloss.Color("#374151")
	Text    = lipgloss.Color("#E5E7EB")
)

var (
	Heading   = lipgloss.NewStyle().Bold(true).Foreground(Accent)
	Label     = lipgloss.NewStyle().Foreground(Accent).Bold(true)
	MutedText = lipgloss.NewStyle().Foreground(Muted)
	OK        = lipgloss.NewStyle().Foreground(Success)
	Err       = lipgloss.NewStyle().Foreground(Danger)
	Warn      = lipgloss.NewStyle().Foreground(Warning)
	Key       = lipgloss.NewStyle().Foreground(Accent).Bold(true)
	Border    = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(Subtle).
			Padding(0, 1)

	OKMark     = OK.Render("✓")
	ErrMark    = Err.Render("✗")
	SkipMark   = MutedText.Render("○")
	RunMark    = Warn.Render("◐")
	PendMark   = MutedText.Render("·")
)
