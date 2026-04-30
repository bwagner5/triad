package cli_test

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bwagner5/triad/pkg/registry"
	"github.com/bwagner5/triad/pkg/ui/cli"
)

func execTyped(t *testing.T, args ...string) (string, error) {
	t.Helper()
	reg := registry.New()
	reg.Register(registry.Resource{
		Name: "job", Plural: "jobs",
		Store: newWidgetStore(),
		Operations: map[string]registry.Operation{
			"run": {
				Name: "run", Short: "run a job",
				Fields: []registry.Field{
					{Flag: "name", Required: true, Help: "name"},
					{Flag: "enabled", Kind: registry.KindBool, Help: "enable job", Default: false},
					{Flag: "timeout", Kind: registry.KindDuration, Help: "job timeout", Default: "30s"},
				},
				Steps: []registry.Step{
					{Label: "check", Do: func(_ context.Context, st *registry.State) error {
						if st.Input.Get("name") != "alpha" {
							return fmt.Errorf("name = %q, want alpha", st.Input.Get("name"))
						}
						enabled, err := st.Input.Bool("enabled")
						if err != nil {
							return err
						}
						if !enabled {
							return fmt.Errorf("enabled = false, want true")
						}
						timeout, err := st.Input.Duration("timeout")
						if err != nil {
							return err
						}
						if timeout != 2*time.Minute {
							return fmt.Errorf("timeout = %s, want 2m", timeout)
						}
						return nil
					}},
				},
			},
		},
	})
	g := &cli.Globals{}
	root := cli.Build("app", "test", reg, g)
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(args)
	err := root.Execute()
	return buf.String(), err
}

func TestCLIStringFieldStillUsesStringFlag(t *testing.T) {
	out, err := execCmd(t, "widget", "create", "--help")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stripANSI(out), "--name string") {
		t.Fatalf("string field help should keep string flag type:\n%s", out)
	}
}

func TestCLITypedBoolAndDurationHelp(t *testing.T) {
	out, err := execTyped(t, "job", "run", "--help")
	if err != nil {
		t.Fatal(err)
	}
	clean := stripANSI(out)
	if !strings.Contains(clean, "--enabled") {
		t.Fatalf("help missing bool flag:\n%s", out)
	}
	if strings.Contains(clean, "--enabled string") {
		t.Fatalf("bool flag should not be rendered as string:\n%s", out)
	}
	if !strings.Contains(clean, "--timeout duration") {
		t.Fatalf("duration field help should use duration flag type:\n%s", out)
	}
}

func TestCLITypedBoolAndDurationParse(t *testing.T) {
	out, err := execTyped(t, "job", "run", "--name", "alpha", "--enabled", "--timeout", "2m", "-y")
	if err != nil {
		t.Fatalf("err=%v out=%s", err, out)
	}
}

func TestCLITypedDurationRejectsInvalidValue(t *testing.T) {
	_, err := execTyped(t, "job", "run", "--name", "alpha", "--enabled", "--timeout", "later", "-y")
	if err == nil || !strings.Contains(err.Error(), "invalid argument") {
		t.Fatalf("err = %v, want cobra duration parse error", err)
	}
}
