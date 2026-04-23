// Package theme defines shared lipgloss v2 styles used by the CLI, wizard,
// and TUI. Override these package-level variables to customize the look of
// all three interfaces at once.
package theme

import "charm.land/lipgloss/v2"

// Colors used throughout the UI. Override these to change the palette.
var (
	Accent  = lipgloss.Color("#7D56F4") // primary brand color (headings, keys)
	Success = lipgloss.Color("#04B575") // success indicators, toasts
	Warning = lipgloss.Color("#F2C14E") // in-progress spinners, warnings
	Danger  = lipgloss.Color("#EF4444") // errors, destructive actions
	Muted   = lipgloss.Color("#6B7280") // secondary text, labels
	Subtle  = lipgloss.Color("#374151") // borders, backgrounds
	Text    = lipgloss.Color("#E5E7EB") // primary text
)

// Styles applied to text elements. Each is a lipgloss.Style that can be
// further customized with .Inherit(), .Copy(), etc.
var (
	Heading   = lipgloss.NewStyle().Bold(true).Foreground(Accent)    // section headings
	Label     = lipgloss.NewStyle().Foreground(Muted)                // field labels, column headers
	Value     = lipgloss.NewStyle().Foreground(Text).Bold(true)      // field values, selected items
	MutedText = lipgloss.NewStyle().Foreground(Muted)                // help text, hints
	OK        = lipgloss.NewStyle().Foreground(Success)              // success messages
	Err       = lipgloss.NewStyle().Foreground(Danger)               // error messages
	Warn      = lipgloss.NewStyle().Foreground(Warning)              // warnings
	Key       = lipgloss.NewStyle().Foreground(Accent).Bold(true)    // key binding labels

	// ToastOK and ToastErr style the flash messages at the top of the TUI.
	ToastOK  = lipgloss.NewStyle().Background(Success).Foreground(lipgloss.Color("#0b0f14")).Bold(true).Padding(0, 2)
	ToastErr = lipgloss.NewStyle().Background(Danger).Foreground(lipgloss.Color("#0b0f14")).Bold(true).Padding(0, 2)

	// Border is the default style for overlay modals (help, palette, saga, wizard).
	Border = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(Subtle).
		Padding(0, 1)

	// PillActive and PillIdle style the breadcrumb tabs at the bottom of the TUI.
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

	// Status marks used in saga step rendering (CLI and TUI).
	OKMark   = OK.Render("✓")
	ErrMark  = Err.Render("✗")
	SkipMark = MutedText.Render("○")
	RunMark  = Warn.Render("◐")
	PendMark = MutedText.Render("·")
)
