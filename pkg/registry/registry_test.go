package registry_test

import (
	"context"
	"strings"
	"testing"
	"time"

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

func TestInputTypedAccessors(t *testing.T) {
	in := registry.Input{
		"name":    "widget",
		"enabled": "true",
		"timeout": "90s",
	}

	if got := in.StringDefault("name", "fallback"); got != "widget" {
		t.Fatalf("StringDefault(name) = %q, want widget", got)
	}
	if got := in.StringDefault("missing", "fallback"); got != "fallback" {
		t.Fatalf("StringDefault(missing) = %q, want fallback", got)
	}
	enabled, err := in.Bool("enabled")
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Fatal("Bool(enabled) = false, want true")
	}
	timeout, err := in.Duration("timeout")
	if err != nil {
		t.Fatal(err)
	}
	if timeout != 90*time.Second {
		t.Fatalf("Duration(timeout) = %s, want 90s", timeout)
	}
}

func TestInputTypedAccessorsInvalidValues(t *testing.T) {
	in := registry.Input{"enabled": "sometimes", "timeout": "soon"}
	if _, err := in.Bool("enabled"); err == nil || !strings.Contains(err.Error(), "enabled") {
		t.Fatalf("Bool invalid err = %v, want field name", err)
	}
	if _, err := in.Duration("timeout"); err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("Duration invalid err = %v, want field name", err)
	}
}

func TestFieldKindValidation(t *testing.T) {
	if err := (registry.Field{Kind: registry.KindBool}).ValidateValue("false"); err != nil {
		t.Fatalf("bool validation failed: %v", err)
	}
	if err := (registry.Field{Kind: registry.KindDuration}).ValidateValue("5m"); err != nil {
		t.Fatalf("duration validation failed: %v", err)
	}
	if err := (registry.Field{Kind: registry.KindBool}).ValidateValue("nope"); err == nil {
		t.Fatal("expected invalid bool error")
	}
	if err := (registry.Field{Kind: registry.KindDuration}).ValidateValue("later"); err == nil {
		t.Fatal("expected invalid duration error")
	}
}

func TestFieldEffectiveKindPreservesLegacyModes(t *testing.T) {
	if got := (registry.Field{}).EffectiveKind(); got != registry.KindString {
		t.Fatalf("empty field kind = %v, want KindString", got)
	}
	if got := (registry.Field{File: true}).EffectiveKind(); got != registry.KindFile {
		t.Fatalf("file field kind = %v, want KindFile", got)
	}
	if got := (registry.Field{Suggest: func(context.Context) ([]registry.Choice, error) {
		return nil, nil
	}}).EffectiveKind(); got != registry.KindChoice {
		t.Fatalf("suggest field kind = %v, want KindChoice", got)
	}
}


func TestInputMulti(t *testing.T) {
	cases := []struct {
		name string
		in   registry.Input
		key  string
		want []string
	}{
		{"empty", registry.Input{}, "x", nil},
		{"single", registry.Input{"x": "a"}, "x", []string{"a"}},
		{"two", registry.Input{"x": "a,b"}, "x", []string{"a", "b"}},
		{"trim whitespace", registry.Input{"x": "a, b , c"}, "x", []string{"a", "b", "c"}},
		{"drop empty", registry.Input{"x": "a,,b,"}, "x", []string{"a", "b"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.in.Multi(c.key)
			if len(got) != len(c.want) {
				t.Fatalf("Multi(%q) = %v; want %v", c.key, got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("Multi(%q)[%d] = %q; want %q", c.key, i, got[i], c.want[i])
				}
			}
		})
	}
}
