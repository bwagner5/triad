package cli

import (
	"fmt"
	"strings"

	"github.com/bwagner5/go-cli-template/pkg/ui/theme"
	"github.com/spf13/cobra"
)

// InstallHelp replaces cobra's default help template with a lipgloss-styled one.
// It applies recursively to every sub-command.
func InstallHelp(root *cobra.Command) {
	root.SetUsageFunc(styledUsage)
	root.SetHelpFunc(func(cmd *cobra.Command, _ []string) {
		fmt.Fprintln(cmd.OutOrStdout(), styledHelp(cmd))
	})
	// Typo suggestions: cobra already emits "did you mean", we wrap its output.
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return fmt.Errorf("%s %w\n\n%s", theme.Err.Render("error:"), err, cmd.UsageString())
	})
}

func styledUsage(cmd *cobra.Command) error {
	fmt.Fprintln(cmd.OutOrStderr(), styledHelp(cmd))
	return nil
}

func styledHelp(cmd *cobra.Command) string {
	var b strings.Builder

	if cmd.Short != "" {
		b.WriteString(theme.Heading.Render(cmd.CommandPath()))
		b.WriteString("  ")
		b.WriteString(theme.MutedText.Render(cmd.Short))
		b.WriteString("\n\n")
	}
	if cmd.Long != "" && cmd.Long != cmd.Short {
		b.WriteString(cmd.Long)
		b.WriteString("\n\n")
	}

	b.WriteString(theme.Label.Render("USAGE"))
	b.WriteString("\n  ")
	b.WriteString(cmd.UseLine())
	b.WriteString("\n\n")

	if cmd.HasAvailableSubCommands() {
		b.WriteString(theme.Label.Render("COMMANDS"))
		b.WriteString("\n")
		max := 0
		for _, sc := range cmd.Commands() {
			if !sc.IsAvailableCommand() {
				continue
			}
			if n := len(sc.Name()); n > max {
				max = n
			}
		}
		for _, sc := range cmd.Commands() {
			if !sc.IsAvailableCommand() {
				continue
			}
			fmt.Fprintf(&b, "  %s  %s\n",
				theme.Key.Render(padRight(sc.Name(), max)),
				theme.MutedText.Render(sc.Short),
			)
		}
		b.WriteString("\n")
	}

	if cmd.HasAvailableLocalFlags() {
		b.WriteString(theme.Label.Render("FLAGS"))
		b.WriteString("\n")
		b.WriteString(indent(cmd.LocalFlags().FlagUsages(), "  "))
		b.WriteString("\n")
	}
	if cmd.HasAvailableInheritedFlags() {
		b.WriteString(theme.Label.Render("GLOBAL FLAGS"))
		b.WriteString("\n")
		b.WriteString(indent(cmd.InheritedFlags().FlagUsages(), "  "))
		b.WriteString("\n")
	}

	if cmd.Example != "" {
		b.WriteString(theme.Label.Render("EXAMPLES"))
		b.WriteString("\n")
		b.WriteString(indent(cmd.Example, "  "))
		b.WriteString("\n")
	}

	return b.String()
}

func padRight(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}

func indent(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, ln := range lines {
		lines[i] = prefix + ln
	}
	return strings.Join(lines, "\n") + "\n"
}
