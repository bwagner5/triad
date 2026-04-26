package tui

import (
	"context"
	"io"
	"reflect"

	tea "charm.land/bubbletea/v2"
	"github.com/bwagner5/triad/pkg/registry"
	"github.com/bwagner5/triad/pkg/runtime"
	"github.com/bwagner5/triad/pkg/trace"
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
//
// Routing rules:
//
//   - Op with Steps: run via saga overlay. Wizard overlay opens first if any
//     required Fields are missing.
//   - Op with Run only and no missing Fields that need wizard input (none
//     Required, or all Required fields are already set via Input): tea.Exec.
//     This releases the tty for streaming output (logs, exec).
//   - Op with Run AND missing wizard-driven inputs: collect via the TUI
//     wizard overlay first, then invoke Run in-process (no tty release).
//     This is how GlobalOps that pick a value end up working — a region
//     switcher doesn't need the terminal, it just needs the picker.
func (a *app) launchOp(res *registry.Resource, op *registry.Operation, input registry.Input) tea.Cmd {
	if input == nil {
		input = registry.Input{}
	}
	missing := missingRequired(op.Fields, input)
	resName := ""
	if res != nil {
		resName = res.Name
	}
	trace.Log("tui.launchOp",
		"resource", resName, "op", op.Name,
		"hasSteps", len(op.Steps) > 0, "hasRun", op.Run != nil,
		"fields", len(op.Fields), "missing", len(missing),
		"confirm", op.Confirm != "", "global", res == nil,
	)

	// Run op with missing inputs → wizard overlay, then run in-process.
	if op.Run != nil && len(op.Steps) == 0 && len(missing) > 0 {
		trace.Log("tui.launchOp.route", "kind", "wizard-then-run")
		return a.wizard.Show(a.ctx, res, op, missing, input)
	}

	// Run op with nothing to prompt for → full tty via tea.Exec.
	if op.Run != nil && len(op.Steps) == 0 {
		trace.Log("tui.launchOp.route", "kind", "tea.Exec")
		cmd := &actionExec{ctx: a.ctx, missing: nil, input: input, op: op}
		return tea.Exec(cmd, func(err error) tea.Msg {
			trace.Log("tui.launchOp.actionDone", "op", op.Name, "err", err)
			return actionDoneMsg{err: err}
		})
	}

	// Saga path: wizard overlay (if needed) → confirm (if set) → saga.
	if len(missing) > 0 {
		trace.Log("tui.launchOp.route", "kind", "wizard-overlay")
		return a.wizard.Show(a.ctx, res, op, missing, input)
	}
	if op.Confirm != "" {
		trace.Log("tui.launchOp.route", "kind", "confirm-overlay")
		a.confirm.Show(op.Confirm, res, op, input)
		return nil
	}
	trace.Log("tui.launchOp.route", "kind", "saga")
	return func() tea.Msg {
		return startSagaMsg{resource: res, op: op, input: input}
	}
}

// startSaga fires a multi-step operation and wires events into the overlay.
// A nil resource is allowed for global ops (region switch etc.) — the saga
// events just carry an empty resource name.
func (a *app) startSaga(msg startSagaMsg) tea.Cmd {
	if msg.err != nil || msg.op == nil {
		trace.Log("tui.startSaga.abort", "err", msg.err, "opNil", msg.op == nil)
		return nil
	}
	var res registry.Resource
	if msg.resource != nil {
		res = *msg.resource
	}
	trace.Log("tui.startSaga", "op", msg.op.Name, "resource", res.Name, "steps", len(msg.op.Steps))
	ch := runtime.Run(a.ctx, a.bus, res, *msg.op, msg.input)
	a.sagaCh = ch
	a.saga.Start(msg.op.Name)
	// Saga owns the screen now — the wizard's busy spinner is obsolete.
	a.wizard.Clear()
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
	trace.Log("tui.actionExec.Run", "op", e.op.Name, "missing", len(e.missing))
	if len(e.missing) > 0 {
		if err := wizard.Collect(e.ctx, e.missing, e.input); err != nil {
			trace.Log("tui.actionExec.wizardErr", "err", err)
			return err
		}
	}
	err := e.op.Run(e.ctx, e.input)
	trace.Log("tui.actionExec.done", "op", e.op.Name, "err", err)
	return err
}
func (e *actionExec) SetStdin(r io.Reader)  { e.stdin = r }
func (e *actionExec) SetStdout(w io.Writer) { e.stdout = w }
func (e *actionExec) SetStderr(w io.Writer) { e.stderr = w }

// actionDoneMsg is posted when a simple action finishes.
type actionDoneMsg struct{ err error }
