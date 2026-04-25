// Package main is triad's demo/reference CLI.
//
// Structure follows Mat Ryer's run-function pattern
// (https://grafana.com/blog/2024/02/09/how-i-write-http-services-in-go-after-13-years/):
// main() only wires os.* into Run(ctx, args, getenv, stdout, stderr) error,
// which is unit-testable. Every env-var read, stdio handle, and exit code
// flows through Run's explicit arguments — nothing else in the package
// touches global OS state.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/bwagner5/triad/pkg/attribution"
	"github.com/bwagner5/triad/pkg/registry"
	"github.com/bwagner5/triad/pkg/resources/container"
	"github.com/bwagner5/triad/pkg/ui/cli"
	"github.com/bwagner5/triad/pkg/ui/tui"
	"github.com/spf13/cobra"
)

var version = "0.0.0-dev"

const cliName = "triad"

func main() {
	ctx := context.Background()
	if err := Run(ctx, os.Args, os.Getenv, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// Run is the testable program entry point.
func Run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()

	reg := registry.New()
	reg.Register(container.Resource())

	g := &cli.Globals{Getenv: getenv}
	root := cli.Build(cliName, "CLI scaffold with CLI + wizard + TUI", reg, g)
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetArgs(args[1:])
	root.Version = "v" + strings.TrimPrefix(version, "v")

	tuiOpts := tui.Options{Name: cliName, Version: root.Version}
	runTUI := func(cmd *cobra.Command, _ []string) error {
		return tui.Run(cmd.Context(), reg, tuiOpts)
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
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprint(cmd.OutOrStdout(), attribution.Text)
			return err
		},
	})
	return root.ExecuteContext(ctx)
}
