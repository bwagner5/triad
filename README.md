# triad

[![Go Reference](https://pkg.go.dev/badge/github.com/bwagner5/triad.svg)](https://pkg.go.dev/github.com/bwagner5/triad)

A Go library for building CLIs with three interface modes from a single resource model:

1. **CLI** — scriptable, with styled `--help`, typo suggestions, and output formats (`short`, `wide`, `yaml`, `json`)
2. **Interactive CLI** — prompts for missing flags with an inline wizard; default mode, disable with `-y`
3. **TUI** — full-window textual UI with table/detail views, CRUD overlays, command palette, and contextual help

Define your resources once. Triad generates the CLI commands, wizard prompts, and TUI screens automatically.

## Install

```bash
go get github.com/bwagner5/triad@latest
```

## Minimal Example

```go
package main

import (
    "context"
    "os"
    "os/signal"

    "github.com/bwagner5/triad/pkg/registry"
    "github.com/bwagner5/triad/pkg/ui/cli"
    "github.com/bwagner5/triad/pkg/ui/tui"
    "github.com/spf13/cobra"
)

func main() {
    reg := registry.New()
    reg.Register(widget.Resource()) // your resource

    g := &cli.Globals{}
    root := cli.Build("my-app", "manage widgets", reg, g)

    // TUI as default command
    runTUI := func(cmd *cobra.Command, _ []string) error {
        return tui.Run(cmd.Context(), reg, tui.Options{Name: "my-app"})
    }
    root.RunE = runTUI
    root.AddCommand(&cobra.Command{
        Use: "tui", Short: "launch TUI", GroupID: "interface", RunE: runTUI,
    })

    ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
    defer stop()
    if err := root.ExecuteContext(ctx); err != nil {
        os.Exit(1)
    }
}
```

This gives you:

```
my-app                        # launches TUI
my-app widget                 # list widgets (table)
my-app widget get <id>        # single widget
my-app widget create          # interactive wizard
my-app widget create -y ...   # non-interactive
my-app widget delete          # with confirmation
my-app widget logs            # custom action
my-app --help                 # styled help with categories
```

## Resource Model

The resource model is the core API. You define a `registry.Resource` and call `Register()`. Triad handles the rest.

### Defining a Resource

```go
package widget

import (
    "context"
    "github.com/bwagner5/triad/pkg/registry"
)

type Widget struct {
    ID     string
    Name   string
    Color  string
    Status string
}

type store struct { /* your backend */ }

func (s *store) Get(ctx context.Context, id string) (any, error)                  { /* ... */ }
func (s *store) List(ctx context.Context, f registry.Filter) ([]any, error)       { /* ... */ }

func Resource() registry.Resource {
    return registry.Resource{
        Name:    "widget",
        Plural:  "widgets",
        Aliases: []string{"w"},
        Short:   "manage widgets",
        Fields:  fields,
        Store:   &store{},
        Operations: ops,
    }
}
```

### Fields

Fields drive CLI flags, wizard prompts, and table columns.

```go
var fields = []registry.Field{
    {Name: "ID",   Flag: "id",   Help: "widget id",   Table: registry.TableHint{Header: "ID"}},
    {Name: "Name", Flag: "name", Short: "n", Help: "widget name", Required: true,
        Validate: func(v string) error {
            if v == "" { return fmt.Errorf("required") }
            return nil
        },
        Table: registry.TableHint{Header: "NAME"},
    },
    {Name: "Status", Flag: "status", Help: "status",
        Table: registry.TableHint{Header: "STATUS", Wide: true}, // only in -o wide
    },
}
```

| Option | Purpose |
|---|---|
| `Required` | CLI errors if missing. Wizard always prompts. |
| `Validate` | Validates the raw string value. |
| `Suggest` | Returns `[]Choice` for selection lists (use for fields referencing existing resources). |
| `Sensitive` | Masks input in the wizard. |
| `Default` | Pre-populates the flag. |
| `Table.Wide` | Only shown with `-o wide`. |

### Operations

Operations are verbs on a resource. An operation with `Steps` is a multi-step workflow with progress UI and rollback. An operation with only `Run` is a simple action.

```go
var ops = map[string]registry.Operation{
    "create": {
        Name: "create", Short: "create a widget", Key: "c",
        Fields: []registry.Field{
            {Flag: "name", Required: true, Help: "widget name"},
        },
        Steps: []registry.Step{
            {Label: "Validate", Do: func(ctx context.Context, s *registry.State) error {
                return nil
            }},
            {Label: "Create", Do: func(ctx context.Context, s *registry.State) error {
                // s.Input.Get("name"), s.Data for passing state between steps
                return nil
            }},
        },
    },
    "delete": {
        Name: "delete", Short: "delete a widget", Key: "ctrl+d",
        Confirm: "Delete this widget?",
        Fields: []registry.Field{
            {Flag: "name", Required: true, Suggest: suggestWidgets},
        },
        Steps: []registry.Step{
            {Label: "Delete", Do: func(ctx context.Context, s *registry.State) error {
                return nil
            }},
        },
    },
    "logs": {
        Name: "logs", Short: "stream logs", Key: "l",
        Fields: []registry.Field{
            {Flag: "name", Required: true, Suggest: suggestWidgets},
        },
        Run: func(ctx context.Context, in registry.Input) error {
            // writes to stdout; TUI releases terminal via tea.Exec
            return nil
        },
    },
}
```

**Multi-step operations** render as: append-only log (CI), live rewriting spinners (interactive CLI), or a progress overlay (TUI).

**Simple actions** run directly to stdout in CLI mode, and temporarily release the terminal in TUI mode.

### Store

```go
type Store interface {
    Get(ctx context.Context, id string) (any, error)
    List(ctx context.Context, f Filter) ([]any, error)
}
```

`List` should return items in a **stable sort order** — the TUI polls periodically and unstable order causes flickering.

## Core TUI Key Bindings

| Key | Action |
|---|---|
| `j`/`k` | Navigate |
| `enter` | Detail view |
| `esc` | Back |
| `/` | Filter |
| `:` | Command palette |
| `?` | Help overlay |
| `r` | Refresh |
| `0-9` | Switch resource |
| `q` | Quit |

Operations with a `Key` field (e.g. `"c"`, `"ctrl+d"`, `"l"`) are automatically bound and shown in the help overlay.

## API Reference

| Package | Purpose |
|---|---|
| `pkg/registry` | `Resource`, `Field`, `Operation`, `Store`, `Input`, `State` |
| `pkg/ui/cli` | `cli.Build()` — cobra command tree with styled help |
| `pkg/ui/tui` | `tui.Run()` — full-screen TUI |
| `pkg/ui/wizard` | `wizard.Collect()` — inline field prompts |
| `pkg/runtime` | Saga executor, event bus, refresh scheduler |
| `pkg/ui/theme` | Shared lipgloss styles |
| `pkg/ui/ascii` | `ascii.Render()` — ASCII art banner from string |

## Example

See [`cmd/triad/`](cmd/triad/) for a working example with a `container` resource that demonstrates fields, operations, a custom action, and a Store.

Try the demo CLI:

```bash
curl -sL "https://github.com/bwagner5/triad/releases/latest/download/triad_$(uname -s)_$(uname -m).tar.gz" | tar xz
./triad --help
./triad              # launches TUI
./triad container    # list containers
```

## License

Apache 2.0 — see [LICENSE](LICENSE).
