# AGENTS.md

## What this is
Triad is a Go library for building CLIs with three UIs from a single resource model: CLI, interactive wizard, and TUI. The public API lives in `pkg/`.

## Commands
- `make ci` — lint + build + test with coverage. Fast enough for the dev loop. Run before every commit.
- `make tools` — installs required dev tools (golangci-lint, goreleaser, gotestsum). Run once, or when tools are outdated. Slower.

## Rules
- **No package-level singletons.** `registry.New()`, `runtime.NewBus()`, explicit `Clock` on `Scheduler`. Don't add `var global = ...` patterns.
- **Tests are integration-flavored, not unit.** See existing tests for style: build real registries, run real sagas, capture real cobra stdout via `SetOut`. Avoid mocking individual functions.
- **Pre-1.0.** Break APIs freely when it improves the design. Remove deprecated code rather than keeping aliases.
- **Minimal changes.** When fixing or extending, touch only what the task requires.

## Layout
- `pkg/registry` — `Resource`, `Field`, `Operation`, `Store` (the model).
- `pkg/runtime` — saga executor, event bus, refresh scheduler.
- `pkg/ui/{cli,wizard,tui}` — the three UIs; all consume `*registry.Registry`.
- `cmd/triad` — demo binary using the `container` resource.
