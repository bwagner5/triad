package tui

import (
	"context"
	"io"
	"reflect"

	tea "charm.land/bubbletea/v2"
	"github.com/bwagner5/go-cli-template/pkg/registry"
	"github.com/bwagner5/go-cli-template/pkg/runtime"
	"github.com/bwagner5/go-cli-template/pkg/ui/wizard"
)

// startSagaMsg is posted after the wizard has collected any missing inputs
// and the saga is ready to run.
type startSagaMsg struct {
	resource *registry.Resource
	saga     *registry.Saga
	input    registry.Input
	err      error
}

// launchSaga is the entry point used by key bindings (`c`, `ctrl+d`) and the
// command palette. It pre-populates `input`, then either shows the wizard
// overlay (for missing required fields), the confirm overlay (when Confirm is
// set and all fields are present), or starts the saga immediately.
func (a *app) launchSaga(res *registry.Resource, saga *registry.Saga, input registry.Input) tea.Cmd {
	if input == nil {
		input = registry.Input{}
	}
	missing := missingRequired(saga.Fields, input)
	if len(missing) > 0 {
		// Show the wizard overlay to collect missing fields.
		return a.wizard.Show(a.ctx, res, saga, missing, input)
	}
	// All fields present. Check if confirmation is needed.
	if saga.Confirm != "" {
		a.confirm.Show(saga.Confirm, res, saga, input)
		return nil
	}
	return func() tea.Msg {
		return startSagaMsg{resource: res, saga: saga, input: input}
	}
}

// startSaga actually fires the saga and wires its events into the overlay.
func (a *app) startSaga(msg startSagaMsg) tea.Cmd {
	if msg.err != nil || msg.resource == nil || msg.saga == nil {
		return nil
	}
	ch := runtime.Run(a.ctx, *msg.resource, *msg.saga, msg.input)
	a.saga.Start(msg.saga.Name)
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

// selectedInput builds an initial Input pre-populating the first Table field
// (typically id or name) from the currently-selected row. It's used for
// delete to skip the prompt when the selection is unambiguous.
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

// launchAction runs a resource Action. Required inputs come from `input`
// and anything missing is collected via the inline wizard. Both the wizard
// (if needed) and the action's Run run as a single tea.Exec so the terminal
// is released once and the user sees a continuous interactive flow.
func (a *app) launchAction(res *registry.Resource, act *registry.Action, input registry.Input) tea.Cmd {
	if input == nil {
		input = registry.Input{}
	}
	missing := missingRequired(act.Fields, input)
	cmd := &actionExec{ctx: a.ctx, missing: missing, input: input, action: act}
	return tea.Exec(cmd, func(err error) tea.Msg {
		return actionDoneMsg{err: err}
	})
}

// actionExec runs (wizard → action.Run) in the foreground terminal.
type actionExec struct {
	ctx     context.Context
	missing []registry.Field
	input   registry.Input
	action  *registry.Action
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
	return e.action.Run(e.ctx, e.input)
}
func (e *actionExec) SetStdin(r io.Reader)  { e.stdin = r }
func (e *actionExec) SetStdout(w io.Writer) { e.stdout = w }
func (e *actionExec) SetStderr(w io.Writer) { e.stderr = w }

// actionDoneMsg is posted when an Action finishes (success or error).
type actionDoneMsg struct{ err error }
