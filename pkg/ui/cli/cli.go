// Package cli wires a Registry into a cobra command tree.
//
// It generates:
//
//	<cli> <resource>                  -> list
//	<cli> <resource> get <id>         -> detail
//	<cli> <resource> <saga>           -> one sub-cmd per saga (create, delete, …)
//	<cli> <resource> <action>         -> one sub-cmd per action (logs, exec, …)
//
// It also installs a lipgloss-rendered help template and typo-suggestion output.
package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/bwagner5/go-cli-template/pkg/registry"
	"github.com/bwagner5/go-cli-template/pkg/runtime"
	"github.com/bwagner5/go-cli-template/pkg/ui/theme"
	"github.com/bwagner5/go-cli-template/pkg/ui/wizard"
	"github.com/spf13/cobra"
)

// Globals carries flags that every command inherits.
type Globals struct {
	Output         string // short | wide | yaml | json
	NonInteractive bool   // true: never prompt, append-only log; false (default): prompt + live view
	Verbose        bool
}

// Interactive returns true when the CLI should prompt for missing inputs
// and render a live saga view.
func (g *Globals) Interactive() bool { return !g.NonInteractive }

// Build constructs the cobra root for the given registry.
func Build(rootUse, short string, reg *registry.Registry, g *Globals) *cobra.Command {
	root := &cobra.Command{
		Use:          rootUse,
		Short:        short,
		SilenceUsage: true,
	}
	root.SetErrPrefix(theme.Err.Render("error:"))
	root.PersistentFlags().StringVarP(&g.Output, "output", "o", "short", "output: short|wide|yaml|json")
	root.PersistentFlags().BoolVarP(&g.NonInteractive, "no-interactive", "y", false, "disable interactive prompts and live progress (for CI / scripts)")
	root.PersistentFlags().BoolVar(&g.Verbose, "verbose", false, "verbose output")

	InstallHelp(root)
	root.SuggestionsMinimumDistance = 2

	for _, res := range reg.All() {
		root.AddCommand(resourceCmd(res, g))
	}
	return root
}

func resourceCmd(res registry.Resource, g *Globals) *cobra.Command {
	c := &cobra.Command{
		Use:     res.Name,
		Aliases: append([]string{res.Plural}, res.Aliases...),
		Short:   res.Short,
		RunE: func(cmd *cobra.Command, args []string) error {
			items, err := res.Store.List(cmd.Context(), registry.Filter{})
			if err != nil {
				return err
			}
			return Render(cmd.OutOrStdout(), g.Output, res, items)
		},
	}

	c.AddCommand(&cobra.Command{
		Use:   "get <id>",
		Short: "get one " + res.Name,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			item, err := res.Store.Get(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return Render(cmd.OutOrStdout(), g.Output, res, []any{item})
		},
	})

	for _, saga := range res.Sagas {
		c.AddCommand(sagaCmd(res, saga, g))
	}
	for _, act := range res.Actions {
		c.AddCommand(actionCmd(res, act, g))
	}
	return c
}

func sagaCmd(res registry.Resource, saga registry.Saga, g *Globals) *cobra.Command {
	in := registry.Input{}
	c := &cobra.Command{
		Use:   saga.Name,
		Short: saga.Short,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := completeInput(cmd.Context(), saga.Fields, in, g.Interactive()); err != nil {
				return err
			}
			return streamSaga(cmd.Context(), res, saga, in, g.Interactive())
		},
	}
	bindFields(c, saga.Fields, in)
	return c
}

func actionCmd(res registry.Resource, act registry.Action, g *Globals) *cobra.Command {
	in := registry.Input{}
	c := &cobra.Command{
		Use:   act.Verb,
		Short: act.Short,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := completeInput(cmd.Context(), act.Fields, in, g.Interactive()); err != nil {
				return err
			}
			return act.Run(cmd.Context(), in)
		},
	}
	bindFields(c, act.Fields, in)
	return c
}

// bindFields wires registry.Field -> cobra flags that write into the Input map.
func bindFields(c *cobra.Command, fields []registry.Field, in registry.Input) {
	// Storage for each flag's string value. We keep them in a slice so
	// each closure captures its own pointer.
	vals := make([]*string, len(fields))
	for i, f := range fields {
		v := ""
		if f.Default != nil {
			v = fmt.Sprintf("%v", f.Default)
			in[f.Flag] = v
		}
		vals[i] = &v
		c.Flags().StringVarP(vals[i], f.Flag, f.Short, v, f.Help)
	}
	// After parsing, copy non-empty flag values into Input.
	prev := c.PreRunE
	fieldsCopy := fields
	c.PreRunE = func(cmd *cobra.Command, args []string) error {
		if prev != nil {
			if err := prev(cmd, args); err != nil {
				return err
			}
		}
		for i, f := range fieldsCopy {
			if cmd.Flags().Changed(f.Flag) && *vals[i] != "" {
				in[f.Flag] = *vals[i]
			}
		}
		return nil
	}
}

// completeInput validates required fields, launching the wizard when allowed.
func completeInput(ctx context.Context, fields []registry.Field, in registry.Input, interactive bool) error {
	missing := []registry.Field{}
	for _, f := range fields {
		if v, ok := in[f.Flag]; ok && v != "" {
			if f.Validate != nil {
				if err := f.Validate(v); err != nil {
					return fmt.Errorf("--%s: %w", f.Flag, err)
				}
			}
			continue
		}
		if f.Required {
			missing = append(missing, f)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	if !interactive {
		var names []string
		for _, f := range missing {
			names = append(names, "--"+f.Flag)
		}
		return fmt.Errorf("missing required flags: %v (pass --no-interactive=false or drop -y to prompt)", names)
	}
	return wizard.Collect(ctx, missing, in)
}

func streamSaga(ctx context.Context, res registry.Resource, saga registry.Saga, in registry.Input, interactive bool) error {
	ch := runtime.Run(ctx, res, saga, in)
	if interactive {
		return RenderEventsLive(ch)
	}
	return RenderEvents(os.Stdout, ch)
}
