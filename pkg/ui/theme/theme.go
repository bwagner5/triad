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
	Label     = lipgloss.NewStyle().Foreground(Muted) // muted k/v label (like k9s "Context:")
	Value     = lipgloss.NewStyle().Foreground(Text).Bold(true)
	MutedText = lipgloss.NewStyle().Foreground(Muted)
	OK        = lipgloss.NewStyle().Foreground(Success)
	Err       = lipgloss.NewStyle().Foreground(Danger)
	Warn      = lipgloss.NewStyle().Foreground(Warning)
	Key       = lipgloss.NewStyle().Foreground(Accent).Bold(true)
	// Toast styles for top-of-screen flash messages.
	ToastOK  = lipgloss.NewStyle().Background(Success).Foreground(lipgloss.Color("#0b0f14")).Bold(true).Padding(0, 2)
	ToastErr = lipgloss.NewStyle().Background(Danger).Foreground(lipgloss.Color("#0b0f14")).Bold(true).Padding(0, 2)
	Border = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(Subtle).
		Padding(0, 1)

	// PillActive / PillIdle are k9s-style breadcrumb tabs rendered at the bottom.
	PillActive = lipgloss.NewStyle().
			Background(lipgloss.Color("#7fd8c4")).
			Foreground(lipgloss.Color("#0b0f14")).
			Bold(true).
			Padding(0, 1)
	PillIdle = lipgloss.NewStyle().
			Background(lipgloss.Color("#f59e0b")).
			Foreground(lipgloss.Color("#0b0f14")).
			Bold(true).
			Padding(0, 1)

	OKMark   = OK.Render("✓")
	ErrMark  = Err.Render("✗")
	SkipMark = MutedText.Render("○")
	RunMark  = Warn.Render("◐")
	PendMark = MutedText.Render("·")
)
