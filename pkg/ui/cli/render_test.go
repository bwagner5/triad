package cli_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/bwagner5/triad/pkg/registry"
	"github.com/bwagner5/triad/pkg/runtime"
	"github.com/bwagner5/triad/pkg/ui/cli"
	"gopkg.in/yaml.v3"
)

type widget struct {
	ID     string
	Name   string
	Status string
}

func widgetResource() registry.Resource {
	return registry.Resource{
		Name: "widget", Plural: "widgets",
		Fields: []registry.Field{
			{Name: "ID", Flag: "id", Table: registry.TableHint{Header: "ID"}},
			{Name: "Name", Flag: "name", Table: registry.TableHint{Header: "NAME"}},
			{Name: "Status", Flag: "status", Table: registry.TableHint{Header: "STATUS", Wide: true}},
		},
	}
}

func widgetItems() []any {
	return []any{
		widget{ID: "w1", Name: "alpha", Status: "running"},
		widget{ID: "w2", Name: "beta", Status: "stopped"},
	}
}

func TestRenderShort(t *testing.T) {
	var buf bytes.Buffer
	if err := cli.Render(&buf, "short", widgetResource(), widgetItems()); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"ID", "NAME", "w1", "alpha", "w2", "beta"} {
		if !strings.Contains(out, want) {
			t.Errorf("short output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "STATUS") || strings.Contains(out, "running") {
		t.Errorf("short output should not include Wide column:\n%s", out)
	}
}

func TestRenderWide(t *testing.T) {
	var buf bytes.Buffer
	if err := cli.Render(&buf, "wide", widgetResource(), widgetItems()); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"STATUS", "running", "stopped"} {
		if !strings.Contains(out, want) {
			t.Errorf("wide output missing %q:\n%s", want, out)
		}
	}
}

func TestRenderJSONRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := cli.Render(&buf, "json", widgetResource(), widgetItems()); err != nil {
		t.Fatal(err)
	}
	var got []widget
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}
	want := []widget{{ID: "w1", Name: "alpha", Status: "running"}, {ID: "w2", Name: "beta", Status: "stopped"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestRenderYAMLRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := cli.Render(&buf, "yaml", widgetResource(), widgetItems()); err != nil {
		t.Fatal(err)
	}
	var got []widget
	if err := yaml.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("invalid YAML: %v\n%s", err, buf.String())
	}
	want := []widget{{ID: "w1", Name: "alpha", Status: "running"}, {ID: "w2", Name: "beta", Status: "stopped"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestRenderUnknownMode(t *testing.T) {
	err := cli.Render(&bytes.Buffer{}, "bogus", widgetResource(), widgetItems())
	if err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Errorf("want error mentioning mode, got %v", err)
	}
}

// ---- §5 RenderEvents ----

func stripANSI(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		if r == 0x1b {
			in = true
			continue
		}
		if in {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				in = false
			}
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func TestRenderEventsFailureSequence(t *testing.T) {
	boom := errors.New("boom")
	ch := make(chan runtime.Event, 5)
	ch <- runtime.Event{Saga: "create", Step: "a", Status: runtime.Running}
	ch <- runtime.Event{Saga: "create", Step: "a", Status: runtime.OK}
	ch <- runtime.Event{Saga: "create", Step: "b", Status: runtime.Running}
	ch <- runtime.Event{Saga: "create", Step: "b", Status: runtime.Failed, Err: boom}
	ch <- runtime.Event{Saga: "create", Status: runtime.Failed, Err: boom, Done: true}
	close(ch)

	var buf bytes.Buffer
	err := cli.RenderEvents(&buf, ch)
	if !errors.Is(err, boom) {
		t.Errorf("RenderEvents err = %v, want %v", err, boom)
	}
	out := stripANSI(buf.String())
	// Step labels appear in order.
	aIdx := strings.Index(out, "a")
	bIdx := strings.Index(out, "b")
	if aIdx < 0 || bIdx < 0 || aIdx > bIdx {
		t.Errorf("step order wrong in:\n%s", out)
	}
	// Terminal state mentions failure.
	if !strings.Contains(out, "failed") || !strings.Contains(out, "boom") {
		t.Errorf("output missing failure markers:\n%s", out)
	}
}
