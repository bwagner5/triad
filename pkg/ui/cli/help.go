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
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), styledHelp(cmd))
	})
	// Typo suggestions: cobra already emits "did you mean", we wrap its output.
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return fmt.Errorf("%s %w\n\n%s", theme.Err.Render("error:"), err, cmd.UsageString())
	})
}

func styledUsage(cmd *cobra.Command) error {
	_, _ = fmt.Fprintln(cmd.OutOrStderr(), styledHelp(cmd))
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
		groups := cmd.Groups()
		if len(groups) > 0 {
			// Render grouped commands.
			for _, g := range groups {
				cmds := groupCommands(cmd, g.ID)
				if len(cmds) == 0 {
					continue
				}
				b.WriteString(theme.Label.Render(strings.ToUpper(g.Title)))
				b.WriteString("\n")
				max := maxNameLen(cmds)
				for _, sc := range cmds {
					fmt.Fprintf(&b, "  %s  %s\n",
						theme.Key.Render(padRight(sc.Name(), max)),
						theme.MutedText.Render(sc.Short),
					)
				}
				b.WriteString("\n")
			}
			// Ungrouped commands (completion, help, etc.).
			ungrouped := groupCommands(cmd, "")
			if len(ungrouped) > 0 {
				b.WriteString(theme.Label.Render("ADDITIONAL COMMANDS"))
				b.WriteString("\n")
				max := maxNameLen(ungrouped)
				for _, sc := range ungrouped {
					fmt.Fprintf(&b, "  %s  %s\n",
						theme.Key.Render(padRight(sc.Name(), max)),
						theme.MutedText.Render(sc.Short),
					)
				}
				b.WriteString("\n")
			}
		} else {
			// No groups defined — flat list.
			b.WriteString(theme.Label.Render("COMMANDS"))
			b.WriteString("\n")
			avail := availableCommands(cmd)
			max := maxNameLen(avail)
			for _, sc := range avail {
				fmt.Fprintf(&b, "  %s  %s\n",
					theme.Key.Render(padRight(sc.Name(), max)),
					theme.MutedText.Render(sc.Short),
				)
			}
			b.WriteString("\n")
		}
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

func availableCommands(cmd *cobra.Command) []*cobra.Command {
	var out []*cobra.Command
	for _, sc := range cmd.Commands() {
		if sc.IsAvailableCommand() {
			out = append(out, sc)
		}
	}
	return out
}

func groupCommands(cmd *cobra.Command, groupID string) []*cobra.Command {
	var out []*cobra.Command
	for _, sc := range cmd.Commands() {
		if !sc.IsAvailableCommand() {
			continue
		}
		if groupID == "" && sc.GroupID == "" {
			out = append(out, sc)
		} else if sc.GroupID == groupID {
			out = append(out, sc)
		}
	}
	return out
}

func maxNameLen(cmds []*cobra.Command) int {
	max := 0
	for _, c := range cmds {
		if n := len(c.Name()); n > max {
			max = n
		}
	}
	return max
}

func indent(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, ln := range lines {
		lines[i] = prefix + ln
	}
	return strings.Join(lines, "\n") + "\n"
}
