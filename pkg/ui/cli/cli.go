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
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"time"

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
	// Debug enables trace logging. DebugFile overrides the default log
	// path (<cliName>-trace.log in cwd). Triad components call
	// trace.Log(...) which is a no-op when disabled.
	Debug     bool
	DebugFile string
	// Prompter overrides the interactive input collector. Nil means use the
	// default wizard-based prompter. Set in tests to a stub.
	Prompter Prompter
	// Getenv lets callers inject env-var resolution (Ryer's run-func
	// pattern). Defaults to os.Getenv if nil. Set in tests to return a
	// canned map so cases can run in parallel.
	Getenv func(string) string
	// cliName is the root command name; used to derive env-var fallbacks
	// for every flag (e.g. LIGHTSAILCTL_REGION). Populated by Build().
	cliName string
}

// getenv returns g.Getenv if set, else os.Getenv as a last-resort fallback.
// Called by env-default helpers; keeps the package testable without
// global state.
func (g *Globals) getenv(key string) string {
	if g.Getenv != nil {
		return g.Getenv(key)
	}
	return os.Getenv(key)
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
		SilenceUsage:  true,
		SilenceErrors: true, // render errors ourselves; cobra would otherwise
	}
	root.SetErrPrefix(theme.Err.Render("error:"))
	root.SetVersionTemplate("{{.Version}}\n")

	// Move --help to global (persistent) flags. Cobra skips adding a
	// local --help when a persistent one already exists.
	root.PersistentFlags().BoolP("help", "h", false, "help for this command")

	// Every persistent flag honors <CLINAME>_<FLAG> as an env-var fallback.
	ge := g.getenv
	outDefault := envOr(ge, rootUse, "output", "short")
	noIntDefault := envBool(ge, rootUse, "no-interactive", false)
	verboseDefault := envBool(ge, rootUse, "verbose", false)
	debugDefault := envBool(ge, rootUse, "debug", false)
	debugFileDefault := envOr(ge, rootUse, "debug-file", rootUse+"-trace.log")
	root.PersistentFlags().StringVarP(&g.Output, "output", "o", outDefault, envHelp(rootUse, "output", "output: short|wide|yaml|json"))
	root.PersistentFlags().BoolVarP(&g.NonInteractive, "no-interactive", "y", noIntDefault, envHelp(rootUse, "no-interactive", "disable interactive prompts and live progress (for CI / scripts)"))
	root.PersistentFlags().BoolVar(&g.Verbose, "verbose", verboseDefault, envHelp(rootUse, "verbose", "verbose output"))
	root.PersistentFlags().BoolVar(&g.Debug, "debug", debugDefault, envHelp(rootUse, "debug", "enable trace logging"))
	root.PersistentFlags().StringVar(&g.DebugFile, "debug-file", debugFileDefault, envHelp(rootUse, "debug-file", "trace log path (when --debug is set)"))

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
		if g.Debug {
			path := g.DebugFile
			if path == "" {
				path = rootUse + "-trace.log"
			}
			if _, err := trace.Enable(path); err != nil {
				return fmt.Errorf("--debug: %w", err)
			}
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "trace log: %s\n", path)
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
			if op.Pre != nil {
				if err := op.Pre(cmd.Context(), in); err != nil {
					return err
				}
			}
			if err := CompleteInput(cmd.Context(), op.Fields, in, g.Interactive(), g.Prompter); err != nil {
				return err
			}
			return streamOp(cmd.Context(), cmd.OutOrStdout(), res, op, in, g.Interactive())
		},
	}
	bindFields(c, op.Fields, in, g)
	return c
}

func actionCmd(_ registry.Resource, op registry.Operation, g *Globals) *cobra.Command {
	in := registry.Input{}
	c := &cobra.Command{
		Use:     op.Name,
		Aliases: op.Aliases,
		Short:   op.Short,
		RunE: func(cmd *cobra.Command, args []string) error {
			if op.Pre != nil {
				if err := op.Pre(cmd.Context(), in); err != nil {
					return err
				}
			}
			if err := CompleteInput(cmd.Context(), op.Fields, in, g.Interactive(), g.Prompter); err != nil {
				return err
			}
			if g.Interactive() {
				return runWithSpinner(cmd.Context(), op.Short, func(ctx context.Context) error {
					return op.Run(ctx, in)
				})
			}
			return op.Run(cmd.Context(), in)
		},
	}
	bindFields(c, op.Fields, in, g)
	return c
}

// bindFields wires registry.Field -> cobra flags that write into the Input map.
func bindFields(c *cobra.Command, fields []registry.Field, in registry.Input, g *Globals) {
	cliName := g.cliName
	// Storage for each flag's string value. We keep them in a slice so
	// each closure captures its own pointer.
	vals := make([]*string, len(fields))
	for i, f := range fields {
		v := ""
		if f.Default != nil {
			v = fmt.Sprintf("%v", f.Default)
		}
		// <CLINAME>_<FLAG> env var overrides the static Default.
		if env := g.getenv(FlagToEnvVar(cliName, f.Flag)); env != "" {
			v = env
			// Only env-var overrides go into Input. Default values are
			// NOT written here — the wizard seeds them via 3-tier
			// precedence (Input > Prefill > Default) so the user still
			// gets prompted. Non-interactive mode applies defaults in
			// CompleteInput.
			in[f.Flag] = env
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

// envOr returns the value of <CLINAME>_<FLAG> if set in getenv, else fallback.
func envOr(getenv func(string) string, cliName, flag, fallback string) string {
	if v := getenv(FlagToEnvVar(cliName, flag)); v != "" {
		return v
	}
	return fallback
}

// envBool reads <CLINAME>_<FLAG> as a bool (1/true/yes). Falls back to def.
func envBool(getenv func(string) string, cliName, flag string, def bool) bool {
	v := getenv(FlagToEnvVar(cliName, flag))
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
	var missing []registry.Field
	for _, f := range fields {
		if v, ok := in[f.Flag]; ok && v != "" {
			if f.Validate != nil {
				if err := f.Validate(v); err != nil {
					return fmt.Errorf("--%s: %w", f.Flag, err)
				}
			}
			continue
		}
		// Hidden from wizard: auto-apply default and skip.
		if f.Wizard != nil && !*f.Wizard {
			if f.Default != nil {
				in[f.Flag] = fmt.Sprintf("%v", f.Default)
			}
			continue
		}
		missing = append(missing, f)
	}
	if len(missing) == 0 {
		return nil
	}
	if !interactive {
		// Non-interactive: apply defaults to satisfy missing fields.
		// Only error on required fields that have no default.
		var stillMissing []string
		for _, f := range missing {
			if f.Default != nil {
				in[f.Flag] = fmt.Sprintf("%v", f.Default)
			} else if f.Required {
				stillMissing = append(stillMissing, "--"+f.Flag)
			}
		}
		if len(stillMissing) > 0 {
			return fmt.Errorf("missing required flags: %v (pass --no-interactive=false or drop -y to prompt)", stillMissing)
		}
		return nil
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

// runWithSpinner shows an inline spinner while fn executes. Used for
// simple Run actions (status, logs) in interactive CLI mode.
func runWithSpinner(ctx context.Context, label string, fn func(context.Context) error) error {
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

	// Capture stdout so fn's output doesn't collide with the spinner.
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		return fn(ctx) // fallback: no spinner
	}
	os.Stdout = w

	var buf bytes.Buffer
	copyDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, r)
		close(copyDone)
	}()

	// Spinner on stderr.
	stop := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		i := 0
		for {
			select {
			case <-stop:
				fmt.Fprintf(os.Stderr, "\r\033[K")
				return
			default:
				fmt.Fprintf(os.Stderr, "\r%s %s", frames[i%len(frames)], label+"…")
				i++
				time.Sleep(80 * time.Millisecond)
			}
		}
	}()

	fnErr := fn(ctx)

	// Stop spinner, restore stdout, flush captured output.
	close(stop)
	<-stopped
	w.Close()
	os.Stdout = origStdout
	<-copyDone
	_, _ = origStdout.Write(buf.Bytes())

	return fnErr
}
