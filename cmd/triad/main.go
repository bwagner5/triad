package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/bwagner5/triad/pkg/attribution"
	"github.com/bwagner5/triad/pkg/registry"
	"github.com/bwagner5/triad/pkg/resources/container"
	"github.com/bwagner5/triad/pkg/ui/cli"
	"github.com/bwagner5/triad/pkg/ui/tui"
	"github.com/spf13/cobra"
)

var version = "dev"

const cliName = "triad"

func main() {
	// 1) Declare resources. This is the main extension point.
	registry.Register(container.Resource())

	// 2) Build the CLI.
	g := &cli.Globals{}
	root := cli.Build(cliName, "CLI scaffold with CLI + wizard + TUI", registry.Default(), g)
	root.Version = version

	// 3) Add the `tui` sub-command, and make it the default when no
	//    sub-command is given.
	tuiOpts := tui.Options{Name: cliName}
	runTUI := func(cmd *cobra.Command, _ []string) error {
		return tui.Run(cmd.Context(), registry.Default(), tuiOpts)
	}
	root.RunE = runTUI
	root.AddCommand(&cobra.Command{
		Use:     "tui",
		Short:   "launch the full-screen TUI",
		GroupID: "interface",
		RunE:    runTUI,
	})
	root.AddCommand(&cobra.Command{
		Use:   "attribution",
		Short: "print open-source dependency licenses",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Print(attribution.Text)
			return nil
		},
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := root.ExecuteContext(ctx); err != nil {
		os.Exit(1)
	}
}
