package tui

import (
	"context"
	"io"
	"reflect"

	tea "charm.land/bubbletea/v2"
	"github.com/bwagner5/triad/pkg/registry"
	"github.com/bwagner5/triad/pkg/runtime"
	"github.com/bwagner5/triad/pkg/ui/wizard"
)

// startSagaMsg is posted when an operation with Steps is ready to run.
type startSagaMsg struct {
	resource *registry.Resource
	op       *registry.Operation
	input    registry.Input
	err      error
}

// launchOp is the unified entry point for all operations. It routes to the
// wizard overlay (missing fields), confirm overlay (destructive ops), saga
// runner (Steps), or tea.Exec (Run).
func (a *app) launchOp(res *registry.Resource, op *registry.Operation, input registry.Input) tea.Cmd {
	if input == nil {
		input = registry.Input{}
	}
	missing := missingRequired(op.Fields, input)

	// Simple action (Run, no Steps): collect missing fields via tea.Exec wizard, then run.
	if op.Run != nil && len(op.Steps) == 0 {
		cmd := &actionExec{ctx: a.ctx, missing: missing, input: input, op: op}
		return tea.Exec(cmd, func(err error) tea.Msg {
			return actionDoneMsg{err: err}
		})
	}

	// Multi-step operation: use TUI overlays.
	if len(missing) > 0 {
		return a.wizard.Show(a.ctx, res, op, missing, input)
	}
	if op.Confirm != "" {
		a.confirm.Show(op.Confirm, res, op, input)
		return nil
	}
	return func() tea.Msg {
		return startSagaMsg{resource: res, op: op, input: input}
	}
}

// startSaga fires a multi-step operation and wires events into the overlay.
func (a *app) startSaga(msg startSagaMsg) tea.Cmd {
	if msg.err != nil || msg.resource == nil || msg.op == nil {
		return nil
	}
	ch := runtime.Run(a.ctx, *msg.resource, *msg.op, msg.input)
	a.saga.Start(msg.op.Name)
	return readSagaCmd(ch)
}

// missingRequired returns fields whose input is not yet provided.
func missingRequired(fields []registry.Field, in registry.Input) []registry.Field {
	var out []registry.Field
	for _, f := range fields {
		if !f.Required {
			continue
		}
		if v, ok := in[f.Flag]; ok && v != "" {
			continue
		}
		out = append(out, f)
	}
	return out
}

// selectedInput builds an initial Input pre-populating table-visible fields
// from the currently-selected row.
func (a *app) selectedInput() registry.Input {
	in := registry.Input{}
	if a.resource == nil || len(a.items) == 0 || a.cursor >= len(a.items) {
		return in
	}
	rv := reflect.Indirect(reflect.ValueOf(a.items[a.cursor]))
	for _, f := range a.resource.Fields {
		if f.Table.Header == "" {
			continue
		}
		in[f.Flag] = read(rv, f)
	}
	return in
}

// actionExec runs (wizard → op.Run) in the foreground terminal via tea.Exec.
type actionExec struct {
	ctx     context.Context
	missing []registry.Field
	input   registry.Input
	op      *registry.Operation
	stdin   io.Reader
	stdout  io.Writer
	stderr  io.Writer
}

func (e *actionExec) Run() error {
	if len(e.missing) > 0 {
		if err := wizard.Collect(e.ctx, e.missing, e.input); err != nil {
			return err
		}
	}
	return e.op.Run(e.ctx, e.input)
}
func (e *actionExec) SetStdin(r io.Reader)  { e.stdin = r }
func (e *actionExec) SetStdout(w io.Writer) { e.stdout = w }
func (e *actionExec) SetStderr(w io.Writer) { e.stderr = w }

// actionDoneMsg is posted when a simple action finishes.
type actionDoneMsg struct{ err error }
