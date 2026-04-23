// Package container is an example resource that ships with the triad demo CLI.
//
// It demonstrates the full resource model:
//   - Fields with validation, table hints, and [registry.SuggestFrom] for selection lists
//   - A [registry.Store] backed by an in-memory map (swap for your real backend)
//   - A multi-step Operation ("create") with Steps and rollback
//   - A destructive Operation ("delete") with Confirm and pre-populated input
//   - A simple action Operation ("logs") with Run for streaming output
//
// Use this as a reference when building your own resources.
package container

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bwagner5/triad/pkg/registry"
)

// Container is the domain type. Struct field names are matched (case-insensitive)
// against Field.Name for table rendering and detail views.
type Container struct {
	ID     string
	Name   string
	Image  string
	Status string
}

// ---- In-memory store (swap this for a real backend) ----

type store struct {
	mu    sync.Mutex
	items map[string]Container
}

func newStore() *store {
	return &store{items: map[string]Container{
		"c1": {ID: "c1", Name: "web", Image: "nginx:1.25", Status: "running"},
		"c2": {ID: "c2", Name: "db", Image: "postgres:16", Status: "running"},
	}}
}

var backend = newStore()
var idSeq atomic.Int64

func init() { idSeq.Store(2) } // c1, c2 are pre-seeded

func (s *store) Get(_ context.Context, id string) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.items {
		if c.ID == id || c.Name == id {
			return c, nil
		}
	}
	return nil, fmt.Errorf("not found: %s", id)
}

func (s *store) List(_ context.Context, f registry.Filter) ([]any, error) {
	time.Sleep(2 * time.Second)
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]any, 0, len(s.items))
	for _, c := range s.items {
		if f.NameLike != "" && !strings.Contains(c.Name, f.NameLike) {
			continue
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].(Container).ID < out[j].(Container).ID
	})
	return out, nil
}

func (s *store) put(c Container) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[c.ID] = c
}

func (s *store) delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, id)
}

// ---- Resource definition ----

// Resource returns the registry.Resource for containers. Call
// reg.Register(container.Resource()) in main to wire it into all UIs.
func Resource() registry.Resource {
	fields := []registry.Field{
		{Name: "ID", Flag: "id", Help: "container id", Table: registry.TableHint{Header: "ID"}},
		{Name: "Name", Flag: "name", Short: "n", Help: "container name", Table: registry.TableHint{Header: "NAME"}},
		{Name: "Image", Flag: "image", Help: "image", Table: registry.TableHint{Header: "IMAGE"}},
		{Name: "Status", Flag: "status", Help: "status", Table: registry.TableHint{Header: "STATUS", Wide: true}},
	}
	suggest := registry.SuggestFrom(backend, fields, "name")
	return registry.Resource{
		Name:    "container",
		Plural:  "containers",
		Aliases: []string{"c", "ctr"},
		Short:   "manage containers",
		Fields:  fields,
		Store:   backend,
		Operations: map[string]registry.Operation{
			"create": {
				Name:  "create",
				Key:   "c",
				Short: "create a container",
				Fields: []registry.Field{
					{Flag: "name", Short: "n", Help: "container name", Required: true, Validate: nonEmpty},
					{Flag: "image", Help: "image to run (e.g. nginx:1.25)", Required: true},
				},
				Steps: []registry.Step{
					{Label: "Validate input", Do: func(_ context.Context, s *registry.State) error {
						if s.Input.Get("name") == "" {
							return fmt.Errorf("name required")
						}
						return nil
					}},
					{Label: "Pull image", Do: func(ctx context.Context, _ *registry.State) error {
						select {
						case <-time.After(600 * time.Millisecond):
						case <-ctx.Done():
							return ctx.Err()
						}
						return nil
					}},
					{Label: "Start container", Do: func(_ context.Context, s *registry.State) error {
						id := fmt.Sprintf("c%d", idSeq.Add(1))
						backend.put(Container{ID: id, Name: s.Input.Get("name"), Image: s.Input.Get("image"), Status: "running"})
						s.Data["id"] = id
						return nil
					}},
				},
			},
			"delete": {
				Name:    "delete",
				Key:     "ctrl+d",
				Short:   "delete a container",
				Confirm: "Delete this container? This cannot be undone.",
				Fields: []registry.Field{
					{Flag: "name", Short: "n", Help: "container name", Required: true, Suggest: suggest},
				},
				Steps: []registry.Step{
					{Label: "Stop container", Do: func(_ context.Context, s *registry.State) error {
						time.Sleep(200 * time.Millisecond)
						return nil
					}},
					{Label: "Remove container", Do: func(_ context.Context, s *registry.State) error {
						name := s.Input.Get("name")
						for _, c := range mustList() {
							if c.Name == name {
								backend.delete(c.ID)
								return nil
							}
						}
						return fmt.Errorf("no container named %q", name)
					}},
				},
			},
			"logs": {
				Name:  "logs",
				Key:   "l",
				Short: "stream logs",
				Fields: []registry.Field{
					{Flag: "name", Short: "n", Help: "container name", Required: true, Suggest: suggest},
					{Flag: "follow", Short: "f", Help: "follow", Default: "false"},
				},
				Run: func(ctx context.Context, in registry.Input) error {
					name := in.Get("name")
					for i := 1; i <= 5; i++ {
						select {
						case <-ctx.Done():
							return ctx.Err()
						case <-time.After(150 * time.Millisecond):
						}
						fmt.Printf("[%s] line %d\n", name, i)
					}
					return nil
				},
			},
		},
	}
}

// ---- Helpers ----

func nonEmpty(v string) error {
	if strings.TrimSpace(v) == "" {
		return fmt.Errorf("must not be empty")
	}
	return nil
}

func mustList() []Container {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	out := make([]Container, 0, len(backend.items))
	for _, c := range backend.items {
		out = append(out, c)
	}
	return out
}
