package cli_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/bwagner5/triad/pkg/registry"
	"github.com/bwagner5/triad/pkg/ui/cli"
)

// TestCLIRunOpReceivesRealStdout is the regression guard for the
// `instance ssh` hang. The CLI used to wrap interactive-mode Run ops
// in a spinner that swapped os.Stdout for an os.Pipe; any op whose
// subprocess inherited os.Stdout (cmd.Stdout = os.Stdout) then wrote
// to the pipe instead of the terminal, so ssh hung on a non-TTY
// stdout. The fix was to stop wrapping. This test pins that down:
// a Run op observes the process's real os.Stdout file descriptor,
// not a pipe swapped in behind its back.
//
// Covers both interactive (default) and non-interactive (-y) modes
// since both paths now go through op.Run directly.
func TestCLIRunOpReceivesRealStdout(t *testing.T) {
	for _, mode := range []struct {
		name string
		args []string
	}{
		{"interactive", []string{"noop", "run", "--name", "alpha"}},
		{"non-interactive", []string{"noop", "run", "--name", "alpha", "-y"}},
	} {
		t.Run(mode.name, func(t *testing.T) {
			realStdoutFd := os.Stdout.Fd()

			var mu sync.Mutex
			var sawFd uintptr
			var sawCalled bool

			reg := registry.New()
			reg.Register(registry.Resource{
				Name: "noop", Plural: "noops", Store: &cliNoopStore{},
				Operations: map[string]registry.Operation{
					"run": {
						Name: "run", Short: "just run",
						Fields: []registry.Field{{Flag: "name", Required: true}},
						Run: func(_ context.Context, _ registry.Input) error {
							mu.Lock()
							sawFd = os.Stdout.Fd()
							sawCalled = true
							mu.Unlock()
							return nil
						},
					},
				},
			})
			g := &cli.Globals{}
			root := cli.Build("app", "test", reg, g)
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&out)
			root.SetArgs(mode.args)
			if err := root.Execute(); err != nil {
				t.Fatalf("%s: execute: %v\nout=%s", mode.name, err, out.String())
			}
			mu.Lock()
			defer mu.Unlock()
			if !sawCalled {
				t.Fatalf("%s: Run never called", mode.name)
			}
			if sawFd != realStdoutFd {
				t.Errorf("%s: op.Run saw os.Stdout fd=%d, want real fd=%d (interactive CLI must not swap stdout for a pipe)",
					mode.name, sawFd, realStdoutFd)
			}
		})
	}
}

// TestCLIInteractiveRunOpErrorPropagates pins that the simplified
// actionCmd still surfaces Run errors to the caller in interactive
// mode — the old runWithSpinner wrapper had its own return path.
func TestCLIInteractiveRunOpErrorPropagates(t *testing.T) {
	wantErr := fmt.Errorf("kaboom")
	reg := registry.New()
	reg.Register(registry.Resource{
		Name: "noop", Plural: "noops", Store: &cliNoopStore{},
		Operations: map[string]registry.Operation{
			"run": {
				Name: "run", Short: "just run",
				Fields: []registry.Field{{Flag: "name", Required: true}},
				Run: func(_ context.Context, _ registry.Input) error {
					return wantErr
				},
			},
		},
	})
	g := &cli.Globals{}
	root := cli.Build("app", "test", reg, g)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"noop", "run", "--name", "alpha"})

	err := root.Execute()
	if err == nil {
		t.Fatalf("expected error from Run; got nil\nout=%s", out.String())
	}
	if !strings.Contains(err.Error(), "kaboom") {
		t.Errorf("err = %v, want a wrap of 'kaboom'", err)
	}
}

// cliNoopStore is a throwaway registry.Store for the ssh regression
// tests above. List/Get never run because the ops under test are Run
// ops that don't touch the Store.
type cliNoopStore struct{}

func (*cliNoopStore) Get(_ context.Context, _ string) (any, error) {
	return nil, fmt.Errorf("not used")
}
func (*cliNoopStore) List(_ context.Context, _ registry.Filter) ([]any, error) {
	return nil, nil
}
