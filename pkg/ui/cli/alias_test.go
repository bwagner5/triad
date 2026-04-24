package cli_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/bwagner5/triad/pkg/registry"
	"github.com/bwagner5/triad/pkg/ui/cli"
)

func TestOperationAliasRuns(t *testing.T) {
	// "widget" fixture below has no alias wired; build a fresh one that does.
	out, err := execAlias(t, []string{"widget", "rm", "--name", "w1", "-y"})
	if err != nil {
		t.Fatalf("err=%v out=%s", err, out)
	}
	if !strings.Contains(stripANSI(out), "complete") {
		t.Errorf("expected saga to run via alias, got:\n%s", out)
	}
}

func TestAliasOpTopLevel(t *testing.T) {
	out, err := execAlias(t, []string{"zap", "--name", "w1", "-y"})
	if err != nil {
		t.Fatalf("err=%v out=%s", err, out)
	}
	if !strings.Contains(stripANSI(out), "complete") {
		t.Errorf("expected top-level AliasOp to run, got:\n%s", out)
	}
}

// execAlias runs a registry that has an op with Aliases + a top-level AliasOp.
func execAlias(t *testing.T, args []string) (string, error) {
	t.Helper()
	reg := registry.New()
	reg.Register(registry.Resource{
		Name: "widget", Plural: "widgets",
		Fields: []registry.Field{
			{Name: "Name", Flag: "name", Table: registry.TableHint{Header: "NAME"}},
		},
		Store: newWidgetStore(),
		Operations: map[string]registry.Operation{
			"delete": {
				Name: "delete", Aliases: []string{"rm"},
				Fields: []registry.Field{{Flag: "name", Required: true}},
				Steps: []registry.Step{
					{Label: "remove", Do: func(_ context.Context, _ *registry.State) error { return nil }},
				},
			},
		},
	})
	g := &cli.Globals{}
	root := cli.Build("app", "test", reg, g)
	// Top-level alias for "widget delete" at path "zap".
	root.AddCommand(cli.AliasOp(reg, g, "widget", "delete", "zap", "zap a widget"))

	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(args)
	err := root.Execute()
	return buf.String(), err
}
