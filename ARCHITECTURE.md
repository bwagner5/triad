# Architecture

This document describes the architecture of `go-cli-template`. It is the blueprint
a CLI author follows when bootstrapping a new project from this scaffold.

## Guiding principles

1. **Three front-ends, one brain.** A non-interactive CLI, an interactive CLI
   (with inline wizards), and a full-screen TUI all call the same business
   logic. Adding a new resource or a new operation never requires touching UI
   code in three places.
2. **Resources are first-class.** A resource is the unit the user thinks in
   (container, cluster, bucket, …). Everything — commands, wizards, TUI
   screens, validation, workflows — is derived from a resource definition.
3. **Workflows (sagas) are explicit.** Any operation that is more than a
   single call is a named sequence of steps. The same saga drives the
   non-interactive output, the interactive spinner line, and the TUI overlay.
4. **The resource package is where the user lives.** 95% of the work of
   building a new CLI happens in `pkg/resources/<name>/`. UI code should
   rarely need to change.

## Layered view

```
┌─────────────────────────────────────────────────────────────────┐
│  Front-ends (UI)                                                │
│  ┌────────────┐  ┌────────────────────┐  ┌───────────────────┐  │
│  │  cli       │  │  cli (interactive) │  │  tui (fullscreen) │  │
│  │  cobra +   │  │  cobra + bubbletea │  │  bubbletea v2 +   │  │
│  │  lipgloss  │  │  inline wizard     │  │  lipgloss v2      │  │
│  └─────┬──────┘  └─────────┬──────────┘  └─────────┬─────────┘  │
│        │                   │                       │            │
│        └───────────────────┴───────────────────────┘            │
│                            │                                    │
├────────────────────────────┼────────────────────────────────────┤
│                            ▼                                    │
│  Resource Registry + Runtime                                    │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │ registry.Registry                                        │   │
│  │   - registered Resource[T] definitions                   │   │
│  │   - lookup by name / alias                               │   │
│  │ runtime.Runtime                                          │   │
│  │   - executes sagas, emits events                         │   │
│  │   - refresh scheduler (fast-after-mutation, slow-steady) │   │
│  └──────────────────────────────────────────────────────────┘   │
│                            │                                    │
├────────────────────────────┼────────────────────────────────────┤
│                            ▼                                    │
│  Business logic  (pkg/resources/<name>/)                        │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │ Resource[T]       — schema, validation, columns          │   │
│  │ Store[T]          — CRUD against real backend            │   │
│  │ Sagas             — create, delete, custom ops           │   │
│  │ Actions           — resource-specific verbs (e.g. logs)  │   │
│  └──────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────┘
```

## Directory layout

```
cmd/
  <cliname>/
    main.go                   # entrypoint, wires registry -> UIs
pkg/
  resources/
    registry/                 # resource registry (generic)
    <name>/                   # one package per resource (container, bucket, …)
      resource.go             # Resource[T] definition
      store.go                # backend CRUD
      sagas.go                # multi-step workflows
      actions.go              # resource-specific actions (logs, exec, …)
      validation.go           # input validation rules
  runtime/
    runtime.go                # saga executor, event bus
    events.go                 # StepStarted / StepOK / StepFailed / …
    refresh.go                # dynamic poll scheduler
  ui/
    cli/                      # non-interactive cobra wiring + pretty --help
      help.go                 # lipgloss v2 help template
      suggest.go              # typo suggestions
      render.go               # table / yaml / json output
    wizard/                   # inline bubbletea v2 wizard
      wizard.go
      prompts/                # text, select, multiselect, confirm, spinner
    tui/                      # full-screen bubbletea v2 app
      app.go                  # root model, screen router
      screens/
        list.go               # generic list screen for any resource
        detail.go             # generic detail screen
      components/
        palette.go            # ":" command palette (fuzzy)
        help_overlay.go       # "?" overlay
        saga_overlay.go       # workflow progress overlay
        statusbar.go          # contextual key-binding bar
      theme/                  # lipgloss v2 styles
  config/                     # global options, config file loading
internal/
  version/
```

## The resource model — where the user works

This is the area a CLI author edits most. Everything else exists to serve it.

### `Resource[T]`

```go
package registry

type Resource[T any] struct {
    // Identity
    Name    string              // "container"
    Plural  string              // "containers"
    Aliases []string            // ["c", "ctr"]
    Short   string              // one-line description

    // Shape (drives rendering + wizard)
    Fields  []Field             // ordered, tagged, validated

    // Store — backend operations
    Store   Store[T]

    // Sagas — named multi-step workflows
    Sagas   map[string]Saga[T]  // "create", "delete", plus custom

    // Actions — resource-specific verbs that are not CRUD
    // e.g. `mycli container logs --name c1`
    Actions map[string]Action[T]
}

type Field struct {
    Name        string     // struct field path
    Flag        string     // cli flag: --name
    Short       string     // short flag: -n
    Help        string
    Required    bool
    Default     any
    Table       TableHint  // {Header:"NAME", Wide:false}
    Validate    func(any) error
    Suggest     func(ctx Context) ([]Choice, error) // for wizard + palette
    Sensitive   bool       // redact in table, mask in wizard
}

type Store[T any] interface {
    Get(ctx context.Context, id string) (T, error)
    List(ctx context.Context, filter Filter) ([]T, error)
    // Create / Delete usually live in Sagas, not here.
}

type Action[T any] struct {
    Verb    string                       // "logs"
    Short   string
    Flags   []Field                      // action-specific flags
    Run     func(ctx Context, target T, args ActionArgs) error
    // Streaming output? Return an io.Reader-like channel; UIs render it.
    Stream  func(ctx Context, target T, args ActionArgs) (<-chan Line, error)
}
```

### Sagas (workflows)

A saga is a named, ordered list of `Step`s. Each step has a label, an idempotent
`Do`, and an optional `Undo` for rollback. Steps emit events as they run; every
UI consumes the same event stream.

```go
type Saga[T any] struct {
    Name  string                         // "create"
    Steps []Step[T]
}

type Step[T any] struct {
    Label string                         // "Validate input"
    Do    func(ctx Context, s *State[T]) error
    Undo  func(ctx Context, s *State[T]) error  // optional
    Skip  func(s *State[T]) bool               // optional
}
```

`runtime.Runtime.Run(saga, input)` returns a `<-chan Event`:

```go
type Event struct {
    Step   string
    Status Status  // Pending | Running | OK | Failed | Skipped
    Err    error
    At     time.Time
}
```

- Non-interactive CLI: prints one line per event, with checkmarks/crosses.
- Interactive CLI: renders an inline bubbletea spinner line that rewrites
  itself per event, then collapses to a summary.
- TUI: opens the saga overlay; each step is a row with a spinner/check/cross.
  Overlay closes a few seconds after final event.

### Adding a custom verb (`mycli container logs`)

```go
res.Actions["logs"] = Action[Container]{
    Verb:  "logs",
    Short: "stream container logs",
    Flags: []Field{
        {Flag: "name", Required: true, Suggest: suggestContainerNames},
        {Flag: "follow", Short: "f", Default: false},
    },
    Stream: func(ctx Context, c Container, a ActionArgs) (<-chan Line, error) {
        return dockerClient.Logs(ctx, c.ID, a.Bool("follow"))
    },
}
```

That single registration produces:
- `mycli container logs --name c1` on the non-interactive CLI.
- A wizard that prompts for `--name` (using `Suggest`) if missing.
- A `:logs` action in the TUI command palette, bound on the container list
  screen, streaming into a pager overlay.

## The three front-ends

### 1. Non-interactive CLI (`pkg/ui/cli`)

- **Library:** `spf13/cobra` for command tree, `charmbracelet/lipgloss/v2` for
  rendering.
- **Pretty `--help`:** custom `cobra.Command.SetHelpFunc` that renders
  commands, flags, and examples using lipgloss v2 styles (headings, muted
  descriptions, bordered usage block, adaptive light/dark colors).
- **Typo suggestions:** cobra already offers Levenshtein suggestions; we
  augment by always printing *both* "command not found" and "did you mean:
  …?", styled with lipgloss. Fallback suggestions come from the resource
  registry (resource names, aliases, action verbs).
- **Output modes:** `-o short|wide|yaml|json`. `short/wide` drive a
  lipgloss-rendered table (replaces today's `olekukonko/tablewriter`).
- **Non-TTY detection:** when stdout is not a TTY, lipgloss styles are
  disabled automatically so output pipes cleanly into jq/awk.

This mode is the default when every required flag is present and `--yes` or
equivalent is used. Missing required flags → error (no wizard), scripted
behavior.

### 2. Interactive CLI with inline wizard (`pkg/ui/wizard`)

- **Library:** `charm.land/bubbletea/v2` (aka `github.com/charmbracelet/bubbletea/v2`),
  running in **inline** mode (`tea.WithAltScreen(false)`), plus
  `charmbracelet/bubbles/v2` for spinner/text/list primitives.
- **Trigger:** chosen when a terminal is a TTY and required input is missing,
  or when the user passes `-i` / `--interactive`.
- **Flow:** cobra parses what it can, then hands control to the wizard with
  the partially-populated struct. The wizard walks remaining `Required`
  fields in order, using `Field.Suggest` to populate choices. While
  `Suggest` is loading, a spinner prompt is shown inline.
- **Prompt primitives:** text input, select, multiselect, confirm, path,
  password (masked), all in-line (single prompt takes 3–6 rows, then is
  replaced by a compact committed line like `✓ name: my-container`).
- **Execution:** once inputs are complete, the wizard runs the same saga
  the non-interactive CLI would run, rendering events as a live inline
  step list.

### 3. Full-screen TUI (`pkg/ui/tui`)

Inspired by k9s.

- **Library:** `charm.land/bubbletea/v2` (alt screen), `lipgloss/v2`,
  `charmbracelet/bubbles/v2`.
- **Top-level model:** a stack of screens with a status bar, theme, refresh
  scheduler, and event bus.
- **Screens:** generic `list` and `detail` work for any registered resource.
  Screen-specific behavior (custom columns, extra actions) is declared on
  the `Resource[T]`, not in UI code.
- **Dynamic refresh:**
    - Steady state: list screens poll at `slowInterval` (default 10s).
    - After a mutation saga completes on a resource, the scheduler bumps
      polling for that resource to `fastInterval` (default 1s) for
      `fastWindow` (default 10s), then decays back.
    - The scheduler exposes `Bump(resourceName)` and subscribes to the
      runtime event bus so any saga success anywhere triggers a bump.
- **Command palette (`:`):** a modal prompt that lists **all** registered
  resources and actions by default, filtered with fuzzy matching
  (`sahilm/fuzzy`) as the user types. Format: `<resource> <verb>`
  (e.g. `container logs`, `bucket create`). Pressing enter either navigates
  (`:containers`), opens a wizard (`:container create`), or runs an action
  (`:container logs <id>`).
- **Contextual help:**
    - Bottom **status bar** shows the 4–6 most important key-bindings for
      the current screen, driven by each screen's `KeyMap()`.
    - **`?` overlay:** dims the screen (renders a semi-transparent layer via
      lipgloss v2 layer compositing) and shows a modal listing **every**
      binding for the screen, grouped (Navigation / Actions / Global).
      The same `?` works on every screen because overlay is a root-level
      component that reads `KeyMap()` from the top screen.
- **Saga overlay:** when a saga starts, the runtime event bus drives a modal
  that lists steps. Each row: `◐ Validate input`, `✓ Create container`,
  `✗ Attach network — permission denied`. Overall status banner appears
  on final event; overlay auto-dismisses 3s after success (stays on
  failure until dismissed).

## Shared glue

### Key maps

Each screen / wizard exposes a `KeyMap` (from `bubbles/v2/key`). The status
bar and `?` overlay both read it, so adding a binding automatically updates
both.

### Theme

`pkg/ui/tui/theme` defines a single lipgloss v2 theme (adaptive light/dark)
consumed by all three front-ends so colours for success/error/muted/accent
are consistent between `mycli create …` output and the TUI.

### Context

A `Context` wrapper carries cancellation, logger, global options, output
writer, and the current UI mode (`cli | wizard | tui`) so a saga step can
tailor output (e.g. never write raw ANSI in `cli` mode when piped).

## Extensibility checklist — adding a new resource

1. Create `pkg/resources/myres/`.
2. Define the Go type `MyRes` and its `Resource[MyRes]`, including `Fields`.
3. Implement `Store[MyRes]`.
4. Write sagas for `create` / `delete` / any custom workflows.
5. Register resource-specific actions (e.g. `logs`, `exec`).
6. In `cmd/<cliname>/main.go`, call `registry.Register(myres.Resource)`.
7. Done. The non-interactive CLI, wizard, and TUI all pick it up.

No UI code is edited for a normal resource. UI code is only touched for
genuinely new UI capabilities (e.g. a new kind of prompt).

## Dependency summary (target versions)

- `github.com/spf13/cobra` — command tree, typo suggestions.
- `github.com/charmbracelet/lipgloss/v2` — styling, layer compositing
  for overlays and dimming.
- `charm.land/bubbletea/v2` — inline wizard and full-screen TUI.
- `github.com/charmbracelet/bubbles/v2` — spinner, textinput, list, key,
  help primitives.
- `github.com/sahilm/fuzzy` — command palette fuzzy matching.
- `gopkg.in/yaml.v3` — config file + yaml output (kept from current template).
- `dario.cat/mergo` — config merge (replaces deprecated `imdario/mergo`).

## Open questions for review

1. **Plugin boundary:** should `Action` support out-of-process plugins (like
   `kubectl`), or is in-process registration enough for v1? in-process only
2. **Streaming output in non-interactive mode:** for actions like `logs`,
   should `-o json` emit NDJSON? No, streaming shouldn't support output modes. 
3. **Resource watch API:** should `Store[T]` optionally expose `Watch()` so
   the TUI list can react to push events instead of polling? (Proposed: add
   optional `Watcher[T]` interface; fall back to polling when not
   implemented.) Ok!
4. **Config file scope:** today's template merges a YAML file into global
   opts; should per-resource sagas also accept a YAML file for bulk create?
   (Proposed: yes, via `mycli <resource> create -f file.yaml`.) sure!
