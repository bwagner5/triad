package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/bwagner5/go-cli-template/pkg/registry"
	"github.com/bwagner5/go-cli-template/pkg/resources/container"
	"github.com/bwagner5/go-cli-template/pkg/ui/cli"
	"github.com/bwagner5/go-cli-template/pkg/ui/tui"
	"github.com/spf13/cobra"
)

var version = "dev"

func main() {
	// 1) Declare resources. This is the main extension point.
	registry.Register(container.Resource())

	// 2) Build the CLI.
	g := &cli.Globals{}
	root := cli.Build("go-cli-template", "CLI scaffold with CLI + wizard + TUI", registry.Default(), g)
	root.Version = version

	// 3) Add the `tui` sub-command, and make it the default when no
	//    sub-command is given.
	runTUI := func(cmd *cobra.Command, _ []string) error {
		return tui.Run(cmd.Context(), registry.Default())
	}
	root.RunE = runTUI
	root.AddCommand(&cobra.Command{
		Use:   "tui",
		Short: "launch the full-screen TUI",
		RunE:  runTUI,
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := root.ExecuteContext(ctx); err != nil {
		os.Exit(1)
	}
}
