package cli_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bwagner5/triad/pkg/registry"
	"github.com/bwagner5/triad/pkg/ui/cli"
)

type stubPrompter struct {
	calls  int
	fill   map[string]string // values to write on Collect
	err    error             // return this error if non-nil
}

func (s *stubPrompter) Collect(_ context.Context, fields []registry.Field, in registry.Input) error {
	s.calls++
	if s.err != nil {
		return s.err
	}
	for _, f := range fields {
		if v, ok := s.fill[f.Flag]; ok {
			in[f.Flag] = v
		}
	}
	return nil
}

func TestCompleteInputAllProvided(t *testing.T) {
	fields := []registry.Field{{Flag: "name", Required: true}}
	in := registry.Input{"name": "foo"}
	p := &stubPrompter{}
	if err := cli.CompleteInput(context.Background(), fields, in, true, p); err != nil {
		t.Fatal(err)
	}
	if p.calls != 0 {
		t.Errorf("prompter invoked when nothing was missing: %d calls", p.calls)
	}
}

func TestCompleteInputMissingNonInteractive(t *testing.T) {
	fields := []registry.Field{
		{Flag: "name", Required: true},
		{Flag: "image", Required: true},
	}
	p := &stubPrompter{}
	err := cli.CompleteInput(context.Background(), fields, registry.Input{}, false, p)
	if err == nil {
		t.Fatal("expected error on missing required flags")
	}
	if !strings.Contains(err.Error(), "--name") || !strings.Contains(err.Error(), "--image") {
		t.Errorf("error should list both flags: %v", err)
	}
	if p.calls != 0 {
		t.Errorf("prompter must not run in non-interactive mode: %d calls", p.calls)
	}
}

func TestCompleteInputMissingInteractiveFills(t *testing.T) {
	fields := []registry.Field{{Flag: "name", Required: true}}
	in := registry.Input{}
	p := &stubPrompter{fill: map[string]string{"name": "foo"}}
	if err := cli.CompleteInput(context.Background(), fields, in, true, p); err != nil {
		t.Fatal(err)
	}
	if in.Get("name") != "foo" {
		t.Errorf("input not filled by prompter: %+v", in)
	}
	if p.calls != 1 {
		t.Errorf("prompter calls = %d, want 1", p.calls)
	}
}

func TestCompleteInputPrompterError(t *testing.T) {
	boom := errors.New("canceled")
	fields := []registry.Field{{Flag: "name", Required: true}}
	p := &stubPrompter{err: boom}
	err := cli.CompleteInput(context.Background(), fields, registry.Input{}, true, p)
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want %v", err, boom)
	}
}

func TestCompleteInputValidateFails(t *testing.T) {
	fields := []registry.Field{{
		Flag: "name", Required: true,
		Validate: func(v string) error { return errors.New("bad") },
	}}
	in := registry.Input{"name": "present"}
	p := &stubPrompter{}
	err := cli.CompleteInput(context.Background(), fields, in, true, p)
	if err == nil || !strings.Contains(err.Error(), "--name") || !strings.Contains(err.Error(), "bad") {
		t.Errorf("want validation error mentioning --name and 'bad', got %v", err)
	}
	if p.calls != 0 {
		t.Errorf("prompter must not run when the provided value fails validation: %d calls", p.calls)
	}
}
