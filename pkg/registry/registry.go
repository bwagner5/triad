// Package registry defines the resource model shared by all three UIs
// (CLI, interactive wizard, and full-screen TUI).
//
// A Resource describes:
//   - a Go type (the resource's shape)
//   - Fields that drive CLI flags, wizard prompts, and table columns
//   - a Store for read operations (Get/List)
//   - Operations for workflows (create, delete) and actions (logs, exec)
//
// Registering a Resource is all it takes to make it available in every UI.
// See [SuggestFrom] for building interactive selection lists from a Store.
package registry

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
)

// Choice is a selectable option offered by Field.Suggest.
type Choice struct {
	Value   string
	Display string
	Help    string
}

// TableHint tells the table renderer how to show a field.
type TableHint struct {
	Header string
	Wide   bool // only shown in -o wide
}

// Field describes a single input / column of a resource.
type Field struct {
	Name      string // struct field path (informational)
	Flag      string // e.g. "name"
	Short     string // e.g. "n"
	Help      string
	Required  bool
	Default   any
	Sensitive bool
	Table     TableHint
	// Validate is called on the raw string form after parsing.
	Validate func(value string) error
	// Suggest returns dynamic choices (e.g. from a backend). May block;
	// UIs will render a spinner while it runs.
	Suggest func(ctx context.Context) ([]Choice, error)
}

// Input is the parsed user input for an operation.
// It's a simple string-keyed map so every UI can produce/consume it.
type Input map[string]string

// Get returns the value for key k, or "" if not set.
func (i Input) Get(k string) string { return i[k] }

// Has reports whether key k has been set (even if empty).
func (i Input) Has(k string) bool { _, ok := i[k]; return ok }

// Filter is passed to Store.List. Free-form; stores interpret fields they care about.
type Filter struct {
	NameLike string
	Limit    int
}

// Store is the read side of a resource.
type Store interface {
	Get(ctx context.Context, id string) (any, error)
	List(ctx context.Context, f Filter) ([]any, error)
}

// State is passed between saga steps. Input is the user's parsed input;
// Data is a scratchpad steps can write to (e.g. a created ID for later steps).
type State struct {
	Input Input
	Data  map[string]any
}

// Step is one unit of work in a Saga.
type Step struct {
	Label string
	Do    func(ctx context.Context, s *State) error
	Undo  func(ctx context.Context, s *State) error // optional, run on failure
	Skip  func(s *State) bool                       // optional
}

// Saga is a named, ordered workflow.
//
// Deprecated: Use Operation instead. Saga is kept as a type alias for migration.
type Saga = Operation

// Action is a resource-specific verb.
//
// Deprecated: Use Operation instead. Action is kept as a type alias for migration.
type Action = Operation

// Operation is a named verb on a resource. It unifies what were previously
// separate Saga and Action types.
//
// An Operation with Steps is a multi-step workflow: the runtime executes each
// step in order, emits progress events, and runs Undo on prior steps if one
// fails. UIs render step-by-step progress (spinners, checkmarks).
//
// An Operation with only Run (no Steps) is a simple action: it executes once,
// typically taking over the terminal for streaming output (e.g. logs, exec).
//
// Both kinds share the same Fields, Key, Confirm, and Short metadata so they
// surface identically in CLI help, the wizard, and TUI key bindings.
type Operation struct {
	Name  string
	Short string
	// Key is an optional TUI key binding (e.g. "c", "ctrl+d", "l"). When set,
	// the TUI dispatches this key to launch the operation. Empty means the
	// operation is available only via the command palette or CLI.
	Key string
	// Confirm, when non-empty, asks the user for explicit confirmation before
	// the operation runs. Use for destructive operations (delete, restart, etc.).
	// The string is the prompt shown to the user.
	Confirm string
	// Fields are the inputs the operation needs. They map to CLI flags / wizard prompts.
	Fields []Field
	// Steps, when non-empty, makes this a multi-step workflow. The runtime
	// executes steps in order and emits progress events. UIs show a progress
	// overlay. If a step fails, prior steps' Undo functions are called in reverse.
	Steps []Step
	// Run, when set (and Steps is empty), makes this a simple action. It
	// executes once and writes directly to stdout. In the TUI, it runs via
	// tea.Exec which temporarily releases the terminal.
	Run func(ctx context.Context, in Input) error
}

// Resource is the central declaration that drives all three UIs.
// Define Fields for columns/flags, a Store for reads, and Operations
// for verbs (create, delete, logs, etc.). Register it with [Register]
// and the CLI commands, wizard prompts, and TUI screens are generated
// automatically.
type Resource struct {
	Name    string
	Plural  string
	Aliases []string
	Short   string
	Fields  []Field // columns + common identifiers
	Store   Store
	// Operations are the verbs available on this resource (create, delete,
	// logs, etc.). Each operation surfaces automatically in the CLI, wizard,
	// and TUI. Operations with Steps render as multi-step workflows;
	// operations with only Run render as simple actions.
	Operations map[string]Operation
}

// Registry is a concurrent-safe collection of Resources.
type Registry struct {
	mu   sync.RWMutex
	list []Resource
	by   map[string]*Resource
}

var global = New()

// Default returns the process-wide registry.
func Default() *Registry { return global }

// New builds an empty registry.
func New() *Registry { return &Registry{by: map[string]*Resource{}} }

// Register adds a resource to the default registry.
func Register(r Resource) { global.Register(r) }

// Register adds a resource. Names and aliases must be unique.
func (r *Registry) Register(res Resource) {
	r.mu.Lock()
	defer r.mu.Unlock()
	keys := append([]string{res.Name, res.Plural}, res.Aliases...)
	for _, k := range keys {
		if k == "" {
			continue
		}
		if _, exists := r.by[k]; exists {
			panic(fmt.Sprintf("registry: duplicate key %q", k))
		}
	}
	r.list = append(r.list, res)
	ref := &r.list[len(r.list)-1]
	for _, k := range keys {
		if k != "" {
			r.by[k] = ref
		}
	}
}

// Lookup returns the resource registered under name/plural/alias, or nil.
func (r *Registry) Lookup(name string) *Resource {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.by[name]
}

// All returns all registered resources, sorted by name.
func (r *Registry) All() []Resource {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Resource, len(r.list))
	copy(out, r.list)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// SuggestFrom builds a Suggest function that lists items from a Store,
// rendering each as a compact table row of visible fields. The valueField
// is the Flag name whose value is returned as Choice.Value (what gets
// committed to Input). All table-visible fields are shown in Display.
func SuggestFrom(store Store, fields []Field, valueField string) func(context.Context) ([]Choice, error) {
	return func(ctx context.Context) ([]Choice, error) {
		items, err := store.List(ctx, Filter{})
		if err != nil {
			return nil, err
		}
		// Collect visible columns.
		type col struct {
			header string
			field  Field
			width  int
		}
		var cols []col
		for _, f := range fields {
			if f.Table.Header == "" || f.Table.Wide {
				continue
			}
			cols = append(cols, col{header: f.Table.Header, field: f, width: len(f.Table.Header)})
		}
		// Build rows and compute max widths.
		rows := make([][]string, len(items))
		for i, item := range items {
			rv := reflect.Indirect(reflect.ValueOf(item))
			row := make([]string, len(cols))
			for j, c := range cols {
				v := fieldValue(rv, c.field)
				row[j] = v
				if len(v) > cols[j].width {
					cols[j].width = len(v)
				}
			}
			rows[i] = row
		}
		// Format helper.
		fmtRow := func(cells []string) string {
			var b strings.Builder
			for j, cell := range cells {
				if j > 0 {
					b.WriteString("  ")
				}
				fmt.Fprintf(&b, "%-*s", cols[j].width, cell)
			}
			return b.String()
		}
		// Build header line.
		hdrs := make([]string, len(cols))
		for j, c := range cols {
			hdrs[j] = c.header
		}
		headerLine := fmtRow(hdrs)
		// Build choices.
		var choices []Choice
		for i, item := range items {
			rv := reflect.Indirect(reflect.ValueOf(item))
			val := ""
			for _, f := range fields {
				if f.Flag == valueField {
					val = fieldValue(rv, f)
					break
				}
			}
			choices = append(choices, Choice{
				Value:   val,
				Display: fmtRow(rows[i]),
				Help:    headerLine,
			})
		}
		return choices, nil
	}
}

func fieldValue(rv reflect.Value, f Field) string {
	for _, k := range []string{f.Name, f.Flag} {
		if k == "" {
			continue
		}
		fv := rv.FieldByNameFunc(func(s string) bool { return strings.EqualFold(s, k) })
		if fv.IsValid() {
			return fmt.Sprintf("%v", fv.Interface())
		}
	}
	return ""
}
