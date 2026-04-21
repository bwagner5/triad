// Package registry defines the resource model shared by every UI.
//
// A Resource describes:
//   - a Go type T (the resource's shape)
//   - Fields that drive CLI flags, wizard prompts, and table columns
//   - a Store for read operations (Get/List)
//   - Sagas for multi-step workflows (create, delete, custom)
//   - Actions for resource-specific verbs like "logs" or "exec"
//
// Registering a Resource is all it takes to make it available in the
// non-interactive CLI, the interactive wizard, and the full-screen TUI.
package registry

import (
	"context"
	"fmt"
	"sort"
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

// Input is the parsed user input for a saga or action.
// It's a simple string-keyed map so every UI can produce/consume it.
type Input map[string]string

func (i Input) Get(k string) string { return i[k] }
func (i Input) Has(k string) bool   { _, ok := i[k]; return ok }

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
type Saga struct {
	Name  string
	Short string
	// Fields are the inputs the saga needs. They map to CLI flags / wizard prompts.
	Fields []Field
	Steps  []Step
}

// Action is a resource-specific verb (e.g. container logs).
type Action struct {
	Verb   string
	Short  string
	Fields []Field
	// Run is invoked with the parsed input. For streaming output, write to ctx.Stdout.
	Run func(ctx context.Context, in Input) error
}

// Resource is the central declaration. Keep Fields, Sagas and Actions
// focused on business logic; UI concerns live elsewhere.
type Resource struct {
	Name    string
	Plural  string
	Aliases []string
	Short   string
	Fields  []Field // columns + common identifiers
	Store   Store
	Sagas   map[string]Saga
	Actions map[string]Action
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
