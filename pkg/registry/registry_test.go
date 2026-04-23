package registry_test

import (
	"testing"

	"github.com/bwagner5/triad/pkg/registry"
)

func TestRegistryLookupAndOrder(t *testing.T) {
	reg := registry.New()
	reg.Register(registry.Resource{Name: "widget", Plural: "widgets", Aliases: []string{"w"}})
	reg.Register(registry.Resource{Name: "apple", Plural: "apples"})

	for _, key := range []string{"widget", "widgets", "w"} {
		if got := reg.Lookup(key); got == nil || got.Name != "widget" {
			t.Errorf("Lookup(%q) did not return widget: %+v", key, got)
		}
	}
	if reg.Lookup("missing") != nil {
		t.Errorf("Lookup(missing) should be nil")
	}

	all := reg.All()
	if len(all) != 2 || all[0].Name != "apple" || all[1].Name != "widget" {
		t.Errorf("All() not sorted by name: %+v", all)
	}
}

func TestRegistryDuplicatePanics(t *testing.T) {
	cases := []struct {
		name string
		res  registry.Resource
	}{
		{"same name", registry.Resource{Name: "widget"}},
		{"plural collides with name", registry.Resource{Name: "other", Plural: "widget"}},
		{"alias collides with name", registry.Resource{Name: "other", Aliases: []string{"widget"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reg := registry.New()
			reg.Register(registry.Resource{Name: "widget", Plural: "widgets"})
			defer func() {
				if recover() == nil {
					t.Errorf("expected panic on duplicate")
				}
			}()
			reg.Register(c.res)
		})
	}
}
