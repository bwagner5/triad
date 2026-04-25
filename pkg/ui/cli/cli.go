// Package cli wires a Registry into a cobra command tree.
//
// It generates:
//
//	<cli> <resource>                  -> list
//	<cli> <resource> get <id>         -> detail
//	<cli> <resource> <op>             -> one sub-cmd per operation (create, delete, logs, …)
//
// It also installs a lipgloss-rendered help template and typo-suggestion output.
package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/bwagner5/triad/pkg/registry"
	"github.com/bwagner5/triad/pkg/runtime"
	"github.com/bwagner5/triad/pkg/trace"
	"github.com/bwagner5/triad/pkg/ui/theme"
	"github.com/bwagner5/triad/pkg/ui/wizard"
	"github.com/spf13/cobra"
)

// Prompter collects values for missing required fields. The default
// implementation (wizard.Collect) launches an inline bubbletea flow;
// tests can inject a stub that auto-fills the Input map.
type Prompter interface {
	Collect(ctx context.Context, fields []registry.Field, in registry.Input) error
}

// prompterFunc adapts a plain function to the Prompter interface.
type prompterFunc func(ctx context.Context, fields []registry.Field, in registry.Input) error

func (f prompterFunc) Collect(ctx context.Context, fields []registry.Field, in registry.Input) error {
	return f(ctx, fields, in)
}

// defaultPrompter is the production prompter backed by wizard.Collect.
var defaultPrompter Prompter = prompterFunc(wizard.Collect)

// Globals carries flags that every command inherits.
type Globals struct {
	Output         string // short | wide | yaml | json
	NonInteractive bool   // true: never prompt, append-only log; false (default): prompt + live view
	Verbose        bool
	// Debug, when non-empty, enables trace logging to that file path.
	// Triad components call trace.Log(...) which is a no-op when disabled.
	Debug string
	// Prompter overrides the interactive input collector. Nil means use the
	// default wizard-based prompter. Set in tests to a stub.
	Prompter Prompter
	// cliName is the root command name; used to derive env-var fallbacks
	// for every flag (e.g. LIGHTSAILCTL_REGION). Populated by Build().
	cliName string
}

// Interactive returns true when the CLI should prompt for missing inputs
// and render a live saga view.
func (g *Globals) Interactive() bool { return !g.NonInteractive }

// Build constructs the cobra root for the given registry.
func Build(rootUse, short string, reg *registry.Registry, g *Globals) *cobra.Command {
	g.cliName = rootUse
	root := &cobra.Command{
		Use:          rootUse,
		Short:        short,
		SilenceUsage: true,
	}
	root.SetErrPrefix(theme.Err.Render("error:"))
	root.SetVersionTemplate("{{.Version}}\n")

	// Every persistent flag honors <CLINAME>_<FLAG> as an env-var fallback.
	outDefault := envOr(rootUse, "output", "short")
	noIntDefault := envBool(rootUse, "no-interactive", false)
	verboseDefault := envBool(rootUse, "verbose", false)
	debugDefault := envOr(rootUse, "debug", "")
	root.PersistentFlags().StringVarP(&g.Output, "output", "o", outDefault, envHelp(rootUse, "output", "output: short|wide|yaml|json"))
	root.PersistentFlags().BoolVarP(&g.NonInteractive, "no-interactive", "y", noIntDefault, envHelp(rootUse, "no-interactive", "disable interactive prompts and live progress (for CI / scripts)"))
	root.PersistentFlags().BoolVar(&g.Verbose, "verbose", verboseDefault, envHelp(rootUse, "verbose", "verbose output"))
	root.PersistentFlags().StringVar(&g.Debug, "debug", debugDefault, envHelp(rootUse, "debug", "write trace log to this file path (empty = off)"))

	// Enable trace logging as soon as flags are parsed, so every subcommand
	// Run sees an active trace writer. PersistentPreRunE wraps any existing
	// one on root so we don't stomp callers that registered their own.
	prevPreRun := root.PersistentPreRunE
	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if prevPreRun != nil {
			if err := prevPreRun(cmd, args); err != nil {
				return err
			}
		}
		if g.Debug != "" {
			if _, err := trace.Enable(g.Debug); err != nil {
				return fmt.Errorf("--debug: %w", err)
			}
		}
		return nil
	}

	InstallHelp(root)
	root.SuggestionsMinimumDistance = 2

	root.AddGroup(
		&cobra.Group{ID: "resources", Title: "Resources:"},
		&cobra.Group{ID: "interface", Title: "Interface:"},
	)

	for _, res := range reg.All() {
		cmd := resourceCmd(res, g)
		cmd.GroupID = "resources"
		root.AddCommand(cmd)
	}
	return root
}

func resourceCmd(res registry.Resource, g *Globals) *cobra.Command {
	list := func(cmd *cobra.Command, args []string) error {
		items, err := res.Store.List(cmd.Context(), registry.Filter{})
		if err != nil {
			return err
		}
		return Render(cmd.OutOrStdout(), g.Output, res, items)
	}
	c := &cobra.Command{
		Use:     res.Name,
		Aliases: append([]string{res.Plural}, res.Aliases...),
		Short:   res.Short,
		RunE:    list,
	}

	// Explicit `list` subcommand so it appears in help. The bare `<resource>`
	// remains a shortcut.
	c.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "list " + res.Plural,
		RunE:  list,
	})

	c.AddCommand(&cobra.Command{
		Use:   "get [id]",
		Short: "get one " + res.Name,
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var id string
			if len(args) > 0 {
				id = args[0]
			} else if g.Interactive() {
				field := registry.Field{
					Flag: "id", Required: true, Help: "select " + res.Name,
					Suggest: registry.SuggestFrom(res.Store, res.Fields, res.Fields[0].Flag),
				}
				in := registry.Input{}
				prompter := g.Prompter
				if prompter == nil {
					prompter = defaultPrompter
				}
				if err := prompter.Collect(cmd.Context(), []registry.Field{field}, in); err != nil {
					return err
				}
				id = in.Get("id")
			} else {
				return fmt.Errorf("id argument required (or drop -y to select interactively)")
			}
			item, err := res.Store.Get(cmd.Context(), id)
			if err != nil {
				return err
			}
			return Render(cmd.OutOrStdout(), g.Output, res, []any{item})
		},
	})

	for _, op := range res.Operations {
		if len(op.Steps) > 0 {
			c.AddCommand(sagaCmd(res, op, g))
		} else if op.Run != nil {
			c.AddCommand(actionCmd(res, op, g))
		}
	}
	return c
}

func sagaCmd(res registry.Resource, op registry.Operation, g *Globals) *cobra.Command {
	in := registry.Input{}
	c := &cobra.Command{
		Use:     op.Name,
		Aliases: op.Aliases,
		Short:   op.Short,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := CompleteInput(cmd.Context(), op.Fields, in, g.Interactive(), g.Prompter); err != nil {
				return err
			}
			return streamOp(cmd.Context(), cmd.OutOrStdout(), res, op, in, g.Interactive())
		},
	}
	bindFields(c, op.Fields, in)
	return c
}

func actionCmd(_ registry.Resource, op registry.Operation, g *Globals) *cobra.Command {
	in := registry.Input{}
	c := &cobra.Command{
		Use:     op.Name,
		Aliases: op.Aliases,
		Short:   op.Short,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := CompleteInput(cmd.Context(), op.Fields, in, g.Interactive(), g.Prompter); err != nil {
				return err
			}
			return op.Run(cmd.Context(), in)
		},
	}
	bindFields(c, op.Fields, in)
	return c
}

// bindFields wires registry.Field -> cobra flags that write into the Input map.
func bindFields(c *cobra.Command, fields []registry.Field, in registry.Input) {
	cliName := rootName(c)
	// Storage for each flag's string value. We keep them in a slice so
	// each closure captures its own pointer.
	vals := make([]*string, len(fields))
	for i, f := range fields {
		v := ""
		if f.Default != nil {
			v = fmt.Sprintf("%v", f.Default)
		}
		// <CLINAME>_<FLAG> env var overrides the static Default.
		if env := os.Getenv(FlagToEnvVar(cliName, f.Flag)); env != "" {
			v = env
		}
		if v != "" {
			in[f.Flag] = v
		}
		vals[i] = &v
		c.Flags().StringVarP(vals[i], f.Flag, f.Short, v, envHelp(cliName, f.Flag, f.Help))
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

// rootName walks up to the root cobra command and returns its Use field —
// the CLI name passed to Build(). Used to derive env-var names for flags
// bound below the root.
func rootName(c *cobra.Command) string {
	for c.HasParent() {
		c = c.Parent()
	}
	return c.Use
}

// envOr returns the value of <CLINAME>_<FLAG> if set, else fallback.
func envOr(cliName, flag, fallback string) string {
	if v := os.Getenv(FlagToEnvVar(cliName, flag)); v != "" {
		return v
	}
	return fallback
}

// envBool reads <CLINAME>_<FLAG> as a bool (1/true/yes). Falls back to def.
func envBool(cliName, flag string, def bool) bool {
	v := os.Getenv(FlagToEnvVar(cliName, flag))
	if v == "" {
		return def
	}
	switch v {
	case "1", "true", "TRUE", "True", "yes", "YES":
		return true
	}
	return false
}

// envHelp appends the expected env var name to a flag's help text.
func envHelp(cliName, flag, help string) string {
	env := FlagToEnvVar(cliName, flag)
	if help == "" {
		return "[$" + env + "]"
	}
	return help + " [$" + env + "]"
}

// CompleteInput validates required fields, launching the prompter when missing.
// If prompter is nil and interactive is true, the default wizard is used.
func CompleteInput(ctx context.Context, fields []registry.Field, in registry.Input, interactive bool, prompter Prompter) error {
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
	if prompter == nil {
		prompter = defaultPrompter
	}
	return prompter.Collect(ctx, missing, in)
}

func streamOp(ctx context.Context, w io.Writer, res registry.Resource, op registry.Operation, in registry.Input, interactive bool) error {
	ch := runtime.Run(ctx, nil, res, op, in)
	if interactive {
		return RenderEventsLive(ch)
	}
	return RenderEvents(w, ch)
}
