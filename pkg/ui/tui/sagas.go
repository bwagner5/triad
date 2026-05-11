package tui

import (
	"context"
	"fmt"
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
	// Refuse to start a second op while one is in flight (including
	// when minimized to the corner pill). The runtime + UI assume one
	// saga at a time; concurrent state would corrupt sagaCh and the
	// overlay's event slice. Surfaces a toast so the user understands
	// why the keypress did nothing instead of just dropping it.
	if a.saga.Active() {
		return a.showToast(toastErr, "another operation is in progress — press - to restore")
	}
	// Run Pre (e.g. hydrate defaults from a config file) so the 'missing'
	// calculation below sees a fully-populated Input. Pre errors bubble
	// up as a toast via the same Failed-event path the saga uses so the
	// user sees what went wrong.
	if op.Pre != nil {
		if err := op.Pre(a.ctx, input); err != nil {
			trace.Trace(a.ctx, "tui launch op pre err", "op", op.Name, "err", err)
			return a.showToast(toastErr, err.Error())
		}
	}
	missing := missingFields(op.Fields, input)
	resName := ""
	if res != nil {
		resName = res.Name
	}
	trace.Trace(a.ctx, "tui launch op",
		"resource", resName, "op", op.Name,
		"hasSteps", len(op.Steps) > 0, "hasRun", op.Run != nil,
		"fields", len(op.Fields), "missing", len(missing),
		"confirm", op.Confirm != "", "global", res == nil,
	)

	// Run op with missing inputs → wizard overlay, then run in-process.
	if op.Run != nil && len(op.Steps) == 0 && len(missing) > 0 {
		trace.Trace(a.ctx, "tui launch op route", "kind", "wizard-then-run")
		return a.wizard.Show(a.ctx, res, op, missing, input)
	}

	// Run op with nothing to prompt for → full tty via tea.Exec.
	if op.Run != nil && len(op.Steps) == 0 {
		trace.Trace(a.ctx, "tui launch op route", "kind", "tea.Exec")
		cmd := &actionExec{ctx: a.ctx, missing: nil, input: input, op: op}
		return tea.Exec(cmd, func(err error) tea.Msg {
			trace.Trace(a.ctx, "tui launch op action done", "op", op.Name, "err", err)
			return actionDoneMsg{err: err}
		})
	}

	// Saga path: wizard overlay (if needed) → confirm (if set) → saga.
	if len(missing) > 0 {
		trace.Trace(a.ctx, "tui launch op route", "kind", "wizard-overlay")
		return a.wizard.Show(a.ctx, res, op, missing, input)
	}
	if op.Confirm != "" {
		trace.Trace(a.ctx, "tui launch op route", "kind", "confirm-overlay")
		a.confirm.Show(op.Confirm, res, op, input)
		return nil
	}
	trace.Trace(a.ctx, "tui launch op route", "kind", "saga")
	return func() tea.Msg {
		return startSagaMsg{resource: res, op: op, input: input}
	}
}

// startSaga fires a multi-step operation and wires events into the overlay.
// A nil resource is allowed for global ops (region switch etc.) — the saga
// events just carry an empty resource name.
func (a *app) startSaga(msg startSagaMsg) tea.Cmd {
	if msg.err != nil || msg.op == nil {
		trace.Trace(a.ctx, "tui start saga abort", "err", msg.err, "opNil", msg.op == nil)
		return nil
	}
	var res registry.Resource
	if msg.resource != nil {
		res = *msg.resource
	}
	trace.Trace(a.ctx, "tui start saga", "op", msg.op.Name, "resource", res.Name, "steps", len(msg.op.Steps))
	ch := runtime.Run(a.ctx, a.bus, res, *msg.op, msg.input)
	a.sagaCh = ch
	a.saga.Start(msg.op.Name, msg.op.Steps)
	// Saga owns the screen now — the wizard's busy spinner is obsolete.
	a.wizard.Clear()
	return readSagaCmd(ch)
}

// missingFields returns fields whose input is not yet provided.
// Includes both required and optional fields so the wizard can prompt
// for everything the operation accepts. Fields with Wizard:false are
// excluded and their Default (if any) is applied to Input automatically.
func missingFields(fields []registry.Field, in registry.Input) []registry.Field {
	var out []registry.Field
	for _, f := range fields {
		// Hidden from wizard: auto-apply default and skip.
		if f.Wizard != nil && !*f.Wizard {
			if f.Default != nil {
				if _, ok := in[f.Flag]; !ok || in[f.Flag] == "" {
					in[f.Flag] = fmt.Sprintf("%v", f.Default)
				}
			}
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
	trace.Trace(e.ctx, "tui action exec run", "op", e.op.Name, "missing", len(e.missing))
	if len(e.missing) > 0 {
		if err := wizard.Collect(e.ctx, e.missing, e.input); err != nil {
			trace.Trace(e.ctx, "tui action exec wizard err", "err", err)
			return err
		}
	}
	err := e.op.Run(e.ctx, e.input)
	trace.Trace(e.ctx, "tui action exec done", "op", e.op.Name, "err", err)
	return err
}
func (e *actionExec) SetStdin(r io.Reader)  { e.stdin = r }
func (e *actionExec) SetStdout(w io.Writer) { e.stdout = w }
func (e *actionExec) SetStderr(w io.Writer) { e.stderr = w }

// actionDoneMsg is posted when a simple action finishes.
type actionDoneMsg struct{ err error }
