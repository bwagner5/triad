package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/bwagner5/triad/pkg/registry"
	"github.com/bwagner5/triad/pkg/ui/cli"
)

// ---- Fixture ----

type widgetStore struct {
	mu    sync.Mutex
	items map[string]widget
}

func newWidgetStore() *widgetStore {
	return &widgetStore{items: map[string]widget{
		"w1": {ID: "w1", Name: "alpha", Status: "running"},
		"w2": {ID: "w2", Name: "beta", Status: "stopped"},
	}}
}

func (s *widgetStore) Get(_ context.Context, id string) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if w, ok := s.items[id]; ok {
		return w, nil
	}
	return nil, fmt.Errorf("not found: %s", id)
}

func (s *widgetStore) List(_ context.Context, _ registry.Filter) ([]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []any{s.items["w1"], s.items["w2"]}
	return out, nil
}

func fixtureResource(store registry.Store) registry.Resource {
	return registry.Resource{
		Name: "widget", Plural: "widgets", Aliases: []string{"w"},
		Short: "manage widgets",
		Fields: []registry.Field{
			{Name: "ID", Flag: "id", Table: registry.TableHint{Header: "ID"}},
			{Name: "Name", Flag: "name", Table: registry.TableHint{Header: "NAME"}},
			{Name: "Status", Flag: "status", Table: registry.TableHint{Header: "STATUS", Wide: true}},
		},
		Store: store,
		Operations: map[string]registry.Operation{
			"create": {
				Name: "create", Short: "create a widget",
				Fields: []registry.Field{
					{Flag: "name", Required: true, Help: "name"},
					{Flag: "image", Required: true, Help: "image"},
				},
				Steps: []registry.Step{
					{Label: "build", Do: func(_ context.Context, _ *registry.State) error { return nil }},
				},
			},
			"logs": {
				Name: "logs", Short: "stream logs",
				Fields: []registry.Field{{Flag: "name", Required: true}},
				Run: func(_ context.Context, in registry.Input) error {
					fmt.Println("logs for", in.Get("name"))
					return nil
				},
			},
		},
	}
}

// execCmd runs root with args, returning stdout+stderr combined and any err.
func execCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	reg := registry.New()
	reg.Register(fixtureResource(newWidgetStore()))
	g := &cli.Globals{}
	root := cli.Build("app", "test", reg, g)
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(args)
	err := root.Execute()
	return buf.String(), err
}

// ---- Tests ----

func TestCLIListShort(t *testing.T) {
	out, err := execCmd(t, "widget", "-y")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ID", "NAME", "w1", "alpha"} {
		if !strings.Contains(out, want) {
			t.Errorf("list missing %q:\n%s", want, out)
		}
	}
}

func TestCLIListJSON(t *testing.T) {
	out, err := execCmd(t, "widget", "-o", "json", "-y")
	if err != nil {
		t.Fatal(err)
	}
	var got []widget
	if jerr := json.Unmarshal([]byte(out), &got); jerr != nil {
		t.Fatalf("invalid JSON: %v\n%s", jerr, out)
	}
	if len(got) != 2 {
		t.Errorf("got %d items, want 2", len(got))
	}
}

func TestCLIGetByID(t *testing.T) {
	out, err := execCmd(t, "widget", "get", "w1", "-y")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "w1") || !strings.Contains(out, "alpha") {
		t.Errorf("get output missing widget: %s", out)
	}
}

func TestCLIGetMissingIDNonInteractive(t *testing.T) {
	_, err := execCmd(t, "widget", "get", "-y")
	if err == nil || !strings.Contains(err.Error(), "id") {
		t.Errorf("want error mentioning id, got %v", err)
	}
}

func TestCLICreateSuccess(t *testing.T) {
	out, err := execCmd(t, "widget", "create", "--name", "foo", "--image", "bar", "-y")
	if err != nil {
		t.Fatalf("err=%v out=%s", err, out)
	}
	if !strings.Contains(stripANSI(out), "complete") {
		t.Errorf("expected 'complete' in saga output:\n%s", out)
	}
}

func TestCLICreateMissingFlags(t *testing.T) {
	_, err := execCmd(t, "widget", "create", "-y")
	if err == nil {
		t.Fatal("expected error on missing required flags")
	}
	msg := err.Error()
	if !strings.Contains(msg, "--name") || !strings.Contains(msg, "--image") {
		t.Errorf("error should list both missing flags: %v", err)
	}
}

func TestCLIAliasesBehaveIdentically(t *testing.T) {
	for _, name := range []string{"widget", "widgets", "w"} {
		out, err := execCmd(t, name, "-y")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.Contains(out, "alpha") {
			t.Errorf("%s list missing row:\n%s", name, out)
		}
	}
}

func TestCLITypoSuggestion(t *testing.T) {
	out, _ := execCmd(t, "widgetz")
	if !strings.Contains(out, "widget") {
		t.Errorf("expected suggestion mentioning 'widget':\n%s", out)
	}
}
