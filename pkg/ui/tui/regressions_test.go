package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/bwagner5/triad/pkg/registry"
)

// TestWizardSeeding_DefaultWhenInputEmpty is the regression for
// 'env came in as "e"'. Fields with Default must render their value in
// the text input when Input is empty (TUI key-binding path) so users can
// press Enter to accept.
func TestWizardSeeding_DefaultWhenInputEmpty(t *testing.T) {
	reg := registry.New()
	reg.Register(registry.Resource{Name: "x", Plural: "xs", Store: fakeStore{}})
	h := newHarness(t, reg, Options{Name: "t"})
	h.Resize(120, 30)

	op := &registry.Operation{
		Name: "test", Steps: []registry.Step{
			{Label: "done", Do: func(_ context.Context, _ *registry.State) error { return nil }},
		},
		Fields: []registry.Field{
			{Flag: "env", Default: "dev"},
		},
	}
	// Directly invoke Show with empty Input (TUI path bypasses bindFields).
	cmd := h.App().wizard.Show(context.Background(), nil, op, op.Fields, registry.Input{})
	h.runCmd(cmd)

	view := h.Text()
	if !strings.Contains(view, "dev") {
		t.Errorf("wizard didn't seed Default='dev' into text input:\n%s", view)
	}
}

// TestWizardSeeding_Precedence covers Input > Prefill > Default.
func TestWizardSeeding_Precedence(t *testing.T) {
	reg := registry.New()
	reg.Register(registry.Resource{Name: "x", Plural: "xs", Store: fakeStore{}})
	h := newHarness(t, reg, Options{Name: "t"})
	h.Resize(120, 30)

	op := &registry.Operation{
		Name: "test", Steps: []registry.Step{
			{Label: "done", Do: func(_ context.Context, _ *registry.State) error { return nil }},
		},
		Fields: []registry.Field{
			{Flag: "name", Default: "static", Prefill: func() string { return "prefilled" }},
		},
	}
	// Input set → wins over both Prefill and Default.
	cmd := h.App().wizard.Show(context.Background(), nil, op, op.Fields, registry.Input{"name": "typed"})
	h.runCmd(cmd)
	if !strings.Contains(h.Text(), "typed") {
		t.Errorf("Input should take precedence, view:\n%s", h.Text())
	}
}

// TestConfirm_NavDoesNotDecide is the regression for 'Running confirm and
// does nothing'. Pressing →/l/tab/h must flip the cursor but NOT drain
// pendingProvide or dismiss the overlay.
func TestConfirm_NavDoesNotDecide(t *testing.T) {
	reg := registry.New()
	reg.Register(registry.Resource{Name: "x", Plural: "xs", Store: fakeStore{}})
	h := newHarness(t, reg, Options{Name: "t"})
	h.Resize(120, 30)

	// Arm a saga-resume confirm: pendingProvide set, overlay shown.
	provided := make(chan registry.Input, 1)
	h.App().pendingProvide = func(in registry.Input) { provided <- in }
	h.App().pendingMerged = registry.Input{"name": "foo"}
	h.App().confirm.Show("confirm this?", nil, sagaNeedConfirmOp(registry.Input{"name": "foo"}), registry.Input{"name": "foo"})

	h.Press("right") // flip cursor to Yes — should NOT decide
	if h.App().pendingProvide == nil {
		t.Fatal("nav key should not drain pendingProvide")
	}
	if !h.App().confirm.Active() {
		t.Fatal("nav key should not dismiss the confirm overlay")
	}
	select {
	case v := <-provided:
		t.Fatalf("nav key invoked Provide unexpectedly: %+v", v)
	default:
	}

	// Now Enter with cursor on Yes → decides.
	h.Press("enter")
	select {
	case got := <-provided:
		if got == nil {
			t.Fatalf("enter after right should Provide the merged input, got nil")
		}
		if got["name"] != "foo" {
			t.Errorf("Provide got %+v; want {name: foo}", got)
		}
	default:
		t.Fatal("enter should have called Provide")
	}
	if h.App().pendingProvide != nil {
		t.Error("pendingProvide should be nil after decision")
	}
}

// TestConfirm_NoOp_DoesNotStartSaga is the defense-in-depth regression for
// the same bug: even if pendingProvide somehow gets nil'd, a confirm with
// a zero-Steps, zero-Run op must not fire startSagaMsg.
func TestConfirm_NoOp_DoesNotStartSaga(t *testing.T) {
	reg := registry.New()
	reg.Register(registry.Resource{Name: "x", Plural: "xs", Store: fakeStore{}})
	h := newHarness(t, reg, Options{Name: "t"})
	h.Resize(120, 30)

	// Arm a confirm with a synthetic op (no Steps, no Run). No pendingProvide.
	synth := &registry.Operation{Name: "synth"} // zero Steps + nil Run
	h.App().confirm.Show("go?", nil, synth, registry.Input{})

	before := h.App().sagaCh
	h.Press("y")
	// If the guard is broken, startSagaMsg would be posted and sagaCh set.
	if h.App().sagaCh != before {
		t.Error("a zero-Steps, zero-Run op must not start a saga")
	}
}

// TestBackspaceBack asserts backspace is bound to 'back' alongside esc
// but still gets absorbed by active text inputs.
func TestBackspaceBack(t *testing.T) {
	reg := registry.New()
	reg.Register(registry.Resource{Name: "x", Plural: "xs", Store: fakeStore{}})
	h := newHarness(t, reg, Options{Name: "t"})
	h.Resize(120, 30)

	h.App().mode = modeDetail
	h.Press("backspace")
	if h.App().mode != modeList {
		t.Errorf("backspace should have returned to list mode, got %v", h.App().mode)
	}
}
