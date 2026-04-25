package cli

import (
	"context"
	"io"

	"github.com/bwagner5/triad/pkg/registry"
	"github.com/bwagner5/triad/pkg/ui/wizard"
)

// wizardExec adapts wizard.Collect into tea.Exec's ExecCommand interface.
// When the live renderer sees a NeedsInput event, it hands the tty to one
// of these and the wizard collects answers without competing with the
// saga's rendering.
type wizardExec struct {
	ctx     context.Context
	need    *registry.NeedInput
	provide func(registry.Input)

	stdin  io.Reader // unused; wizard reads from os.Stdin
	stdout io.Writer // unused
	stderr io.Writer // unused
}

func (w *wizardExec) Run() error {
	in := registry.Input{}
	err := wizard.Collect(w.ctx, w.need.Fields, in)
	if err != nil {
		w.provide(nil) // abort
		return err
	}
	w.provide(in)
	return nil
}

func (w *wizardExec) SetStdin(r io.Reader)  { w.stdin = r }
func (w *wizardExec) SetStdout(wr io.Writer) { w.stdout = wr }
func (w *wizardExec) SetStderr(wr io.Writer) { w.stderr = wr }
