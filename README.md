# go-cli-template

A batteries-included Go CLI scaffold with three interface modes that share a single resource model:

1. **CLI** — non-interactive, scriptable, with styled `--help`, typo suggestions, and multiple output formats (`short`, `wide`, `yaml`, `json`).
2. **Interactive CLI** — same CLI, but prompts for missing flags with an inline wizard (selection lists for known resources, free-form text for everything else). Default mode; disable with `-y` for CI.
3. **TUI** — full-screen k9s-style terminal UI with table views, detail views, CRUD overlays, a command palette, contextual help, toast notifications, and live saga progress.

All three modes are driven by the same `registry.Resource` declarations. You define your resources once; the CLI, wizard, and TUI are generated automatically.

## Quick Start

```bash
# Clone the template
git clone https://github.com/bwagner5/go-cli-template.git my-cli
cd my-cli

# Rename everything from go-cli-template to your CLI name
export CLI_NAME="my-cli"
export GITHUB_OWNER="my-org"

# Replace the module path and all references
find . -path ./.git -prune -o -type f -print0 | xargs -0 sed -i'' -e "s|bwagner5/go-cli-template|${GITHUB_OWNER}/${CLI_NAME}|g"
find . -path ./.git -prune -o -type f -print0 | xargs -0 sed -i'' -e "s|go-cli-template|${CLI_NAME}|g"

# Rename the cmd directory
mv cmd/go-cli-template "cmd/${CLI_NAME}"

# Clean up sed backup files (macOS creates these with -i'')
find . -name "*-e" -type f -delete

# Update the go module
go mod edit -module "github.com/${GITHUB_OWNER}/${CLI_NAME}"
go mod tidy

# Verify
go build ./...
go run "./cmd/${CLI_NAME}" --help
```

## Usage

```
# Launch the TUI (default when no command is given)
my-cli

# Explicitly launch the TUI
my-cli tui

# List resources
my-cli container
my-cli container list

# Get a single resource
my-cli container get c1

# Create (interactive — prompts for missing fields)
my-cli container create

# Create (non-interactive — all flags required)
my-cli container create -y --name web --image nginx:1.25

# Delete (with confirmation prompt)
my-cli container delete --name web

# Resource-specific action
my-cli container logs --name web

# Output formats
my-cli container -o wide
my-cli container -o yaml
my-cli container -o json
```

## TUI Key Bindings

| Key | Action |
|---|---|
| `j` / `k` | Navigate up/down |
| `enter` | View detail |
| `esc` | Back to list |
| `c` | Create (opens wizard overlay) |
| `ctrl+d` | Delete (with confirmation overlay) |
| `l` | Logs (container-specific) |
| `r` | Refresh |
| `:` | Command palette (switch resources) |
| `?` | Help overlay |
| `0-9` | Switch to resource by index |
| `q` | Quit |

## Project Structure

```
cmd/<cli-name>/main.go      # Wires resources into the CLI + TUI
pkg/
  registry/                  # Resource model: Field, Operation, Store
  resources/
    container/               # Example resource (replace with yours)
  runtime/                   # Saga executor, event bus, refresh scheduler
  ui/
    cli/                     # Cobra command tree, styled help, output renderers
    wizard/                  # Inline bubbletea wizard for interactive CLI
    tui/                     # Full-screen TUI (k9s-style)
    theme/                   # Shared lipgloss styles
    ascii/                   # Auto-generated ASCII banner
  duration/                  # Human-friendly duration formatting
```

## Working with the Resource Model

The resource model is the core abstraction. You define resources in `pkg/resources/<name>/` and register them in `main.go`. Everything else — CLI commands, wizard prompts, TUI screens — is generated from the resource definition.

### Defining a Resource

A resource is a `registry.Resource` struct. Here's the anatomy:

```go
package myresource

import (
    "context"
    "github.com/my-org/my-cli/pkg/registry"
)

// 1. Define your Go type
type Widget struct {
    ID     string
    Name   string
    Color  string
    Status string
}

// 2. Implement the Store interface
type store struct { /* your backend */ }

func (s *store) Get(ctx context.Context, id string) (any, error) { /* ... */ }
func (s *store) List(ctx context.Context, f registry.Filter) ([]any, error) { /* ... */ }

// 3. Return the Resource declaration
func Resource() registry.Resource {
    return registry.Resource{
        Name:    "widget",
        Plural:  "widgets",
        Aliases: []string{"w"},
        Short:   "manage widgets",
        Fields:  []registry.Field{ /* ... */ },
        Store:   &store{},
        Operations: map[string]registry.Operation{ /* ... */ },
    }
}
```

Then register it in `main.go`:

```go
registry.Register(myresource.Resource())
```

That's it. The CLI gets `my-cli widget`, `my-cli widget create`, etc. The TUI gets a new screen. The wizard knows how to prompt for fields.

### Fields

Fields serve triple duty: CLI flags, wizard prompts, and table columns.

```go
Fields: []registry.Field{
    {
        Name:  "ID",                              // Struct field name (for reflection)
        Flag:  "id",                               // CLI flag: --id
        Help:  "widget id",                        // Help text shown in --help and wizard
        Table: registry.TableHint{Header: "ID"},   // Column header in table output
    },
    {
        Name:     "Name",
        Flag:     "name",
        Short:    "n",                             // Short flag: -n
        Help:     "widget name",
        Required: true,                            // Wizard will prompt; CLI will error if missing
        Validate: func(v string) error {           // Client-side validation
            if v == "" { return fmt.Errorf("required") }
            return nil
        },
        Table: registry.TableHint{Header: "NAME"},
    },
    {
        Name:  "Status",
        Flag:  "status",
        Help:  "status",
        Table: registry.TableHint{Header: "STATUS", Wide: true},  // Only shown with -o wide
    },
}
```

Key field options:

| Option | Purpose |
|---|---|
| `Required` | CLI errors if missing (unless interactive). Wizard always prompts. |
| `Validate` | Called on the raw string value. Return an error to reject. |
| `Suggest` | Returns `[]Choice` for selection lists. Use for fields referencing existing resources (e.g. picking a container to delete). Do NOT use for free-form input like image names. |
| `Sensitive` | Masks input in the wizard (password mode). |
| `Default` | Pre-populates the flag value. |
| `Table.Wide` | Column only appears in `-o wide` output. |

### Operations (Workflows & Actions)

Operations are the verbs on a resource — create, delete, logs, restart, etc. An operation with `Steps` is a multi-step workflow (progress overlay, rollback on failure). An operation with only `Run` is a simple action (takes over the terminal for streaming output).

```go
Operations: map[string]registry.Operation{
    "create": {
        Name:  "create",
        Short: "create a widget",
        Key:   "c",                    // TUI key binding (press 'c' to create)
        Fields: []registry.Field{      // Inputs needed
            {Flag: "name", Required: true, Help: "widget name"},
            {Flag: "color", Help: "widget color (e.g. red, blue)"},
        },
        Steps: []registry.Step{        // Multi-step workflow
            {
                Label: "Validate input",
                Do: func(ctx context.Context, s *registry.State) error {
                    if s.Input.Get("name") == "" {
                        return fmt.Errorf("name required")
                    }
                    return nil
                },
            },
            {
                Label: "Create widget",
                Do: func(ctx context.Context, s *registry.State) error {
                    id := createWidget(s.Input.Get("name"), s.Input.Get("color"))
                    s.Data["id"] = id
                    return nil
                },
                Undo: func(ctx context.Context, s *registry.State) error {
                    // Optional: rollback on failure of a later step
                    deleteWidget(s.Data["id"].(string))
                    return nil
                },
            },
        },
    },
    "delete": {
        Name:    "delete",
        Short:   "delete a widget",
        Key:     "ctrl+d",
        Confirm: "Delete this widget? This cannot be undone.",  // Confirmation prompt
        Fields: []registry.Field{
            {Flag: "name", Required: true, Suggest: suggestWidgetNames},
        },
        Steps: []registry.Step{
            {Label: "Remove widget", Do: func(ctx context.Context, s *registry.State) error {
                return deleteWidget(s.Input.Get("name"))
            }},
        },
    },
    "logs": {
        Name:  "logs",
        Short: "stream widget logs",
        Key:   "l",
        Fields: []registry.Field{
            {Flag: "name", Required: true, Suggest: suggestWidgetNames},
            {Flag: "follow", Short: "f", Help: "follow log output", Default: "false"},
        },
        Run: func(ctx context.Context, in registry.Input) error {
            // Simple action: write directly to stdout. In the TUI, this
            // runs via tea.Exec which temporarily releases the terminal.
            return streamLogs(ctx, in.Get("name"), in.Get("follow") == "true")
        },
    },
},
```

How operations render across UIs:

**Multi-step (Steps):**
- **CLI (non-interactive / `-y`)**: each step prints `◐ Running` then `✓ Complete` on separate lines.
- **CLI (interactive, default)**: bubbletea live view rewrites lines in-place with spinners.
- **TUI**: saga overlay with step-by-step progress, auto-dismiss on completion.

**Simple action (Run):**
- **CLI**: runs directly, output goes to stdout.
- **TUI**: temporarily releases the terminal via `tea.Exec`, runs the action, then resumes.

### Store (Read Operations)

The `Store` interface is minimal:

```go
type Store interface {
    Get(ctx context.Context, id string) (any, error)
    List(ctx context.Context, f Filter) ([]any, error)
}
```

Tips:
- `List` should return items in a **stable sort order** (the TUI polls periodically; unstable order causes visual flickering).
- `Get` should accept both ID and name for convenience.
- The `Filter` struct has `NameLike` and `Limit` — implement what makes sense for your backend.

### Putting It All Together

Here's the minimal flow to add a new resource:

1. Create `pkg/resources/widget/widget.go`
2. Define the `Widget` struct, a `Store`, and a `Resource()` function
3. In `main.go`, add `registry.Register(widget.Resource())`
4. Run `go build ./...`

You now have:
- `my-cli widget` / `my-cli widget list` — table output
- `my-cli widget get <id>` — single item
- `my-cli widget create` — interactive wizard or `--flag` based
- `my-cli widget delete` — with confirmation
- `my-cli widget logs` — or whatever actions you defined
- TUI screen with all of the above accessible via key bindings
- `--help` on every command with styled output

### Customizing the TUI Banner

The TUI auto-generates an ASCII art banner from the CLI name using [go-figure](https://github.com/common-nighthawk/go-figure). To override it:

```go
tui.Run(ctx, reg, tui.Options{
    Name: "my-cli",
    Logo: lipgloss.NewStyle().Foreground(lipgloss.Color("#F2C14E")).Render(myCustomArt),
})
```

## Building & Releasing

```bash
make build          # Build to ./build/
make test           # Run tests
make lint           # Run golangci-lint (v2 config)
make attribution    # Generate ATTRIBUTION.md from dependency licenses
make release        # Local snapshot release via goreleaser (no publish)
make clean          # Remove build artifacts
```

### Dev Loop

```bash
# Build and run
make build && ./build/go-cli-template --help

# Run directly during development
go run ./cmd/go-cli-template container

# Run tests
make test

# Lint (install golangci-lint first: https://golangci-lint.run/docs/welcome/install/)
make lint
```

### Releasing

Releases are driven by [goreleaser](https://goreleaser.com/) via GitHub Actions. Tag a version to trigger a release:

```bash
git tag v0.1.0
git push origin v0.1.0
```

This builds binaries for linux/darwin × amd64/arm64, creates archives, and publishes them as GitHub release assets with a changelog.

To test the release locally without publishing:

```bash
make release
ls dist/
```

### Attribution

The CLI ships with an `--attribution` flag that prints open-source dependency licenses. To regenerate after changing dependencies:

```bash
make attribution    # generates ATTRIBUTION.md and copies it into the embed directory
go run ./cmd/go-cli-template --attribution
```

## Installation

Download from [GitHub Releases](https://github.com/<owner>/<cli-name>/releases):

```bash
[[ $(uname -m) == "aarch64" ]] && ARCH="arm64" || ARCH="amd64"
OS=$(uname | tr '[:upper:]' '[:lower:]')
VERSION="0.1.0"
curl -sL "https://github.com/<owner>/<cli-name>/releases/download/v${VERSION}/<cli-name>_${VERSION}_${OS}_${ARCH}.tar.gz" | tar xz
chmod +x <cli-name>
```

## License

Apache 2.0 — see [LICENSE](LICENSE).
