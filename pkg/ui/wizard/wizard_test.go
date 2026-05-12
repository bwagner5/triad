package wizard

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/bwagner5/triad/pkg/registry"
)

func TestTypedFieldWithSuggestStillUsesSelectMode(t *testing.T) {
	m := newModel(context.Background(), []registry.Field{{
		Flag: "enabled",
		Kind: registry.KindBool,
		Suggest: func(context.Context) ([]registry.Choice, error) {
			return []registry.Choice{{Value: "true"}, {Value: "false"}}, nil
		},
	}}, registry.Input{})

	if !m.isSelect() {
		t.Fatal("typed field with Suggest should render as a selection list")
	}
}

func TestKindFileUsesFileMode(t *testing.T) {
	m := newModel(context.Background(), []registry.Field{{
		Flag: "path",
		Kind: registry.KindFile,
	}}, registry.Input{})

	if !m.isFile() {
		t.Fatal("KindFile should render as a file picker")
	}
}

// TestShiftTabPreservesTypedValue is the regression for the shift+tab
// "fix my typo" bug: navigating back to a previously-answered field
// must show the user's previous answer in the input so they can edit
// it, instead of resetting to default.
func TestShiftTabPreservesTypedValue(t *testing.T) {
	fields := []registry.Field{{Flag: "a"}, {Flag: "b"}}
	m := newModel(context.Background(), fields, registry.Input{})

	// Init the model so the textinput is focused and ready.
	_ = m.Init()

	// Type "foo" into field 0 and press enter to commit.
	for _, r := range "foo" {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	// Now on field 1: type "bar" and press enter.
	for _, r := range "bar" {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}

	// Press shift+tab to go back to field 0. User expects to see "foo"
	// ready to edit — NOT a reset to the empty input or to a default.
	m.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})

	if got := m.state.Idx(); got != 0 {
		t.Fatalf("after shift+tab, idx = %d; want 0", got)
	}
	if got := m.ti.Value(); got != "foo" {
		t.Errorf("after shift+tab, textinput value = %q; want %q (the previously-typed answer)", got, "foo")
	}
}

// TestCtrlTSetsSwitchToTUIFlag asserts that Ctrl+T pressed during the
// CLI wizard sets the switchTUI flag and quits, so Collect can return
// SwitchToTUI to the caller for a TUI handoff.
func TestCtrlTSetsSwitchToTUIFlag(t *testing.T) {
	fields := []registry.Field{{Flag: "a"}}
	m := newModel(context.Background(), fields, registry.Input{})
	_ = m.Init()
	for _, r := range "partial" {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	m.Update(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	if !m.switchTUI {
		t.Fatal("Ctrl+T should set switchTUI=true")
	}
	// State should have captured the in-flight typing so the TUI can
	// resume with the partial value visible.
	if got := m.state.Entry(0).Text; got != "partial" {
		t.Errorf("state.Entry(0).Text = %q; want partial (in-flight typing should be mirrored on Ctrl+T)", got)
	}
}

// TestShiftTabPreservesDownstreamAnswer asserts that going back does
// not wipe a downstream answer the user already typed — so they can
// fix a typo upstream and continue without re-entering everything.
func TestShiftTabPreservesDownstreamAnswer(t *testing.T) {
	fields := []registry.Field{{Flag: "a"}, {Flag: "b"}}
	m := newModel(context.Background(), fields, registry.Input{})
	_ = m.Init()

	// Commit "foo" to field 0.
	for _, r := range "foo" {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	// Commit "bar" to field 1.
	for _, r := range "bar" {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	// Walk back to field 0.
	m.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	m.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})

	if got := m.state.Entry(1).Text; got != "bar" {
		t.Errorf("downstream answer should survive shift+tab, got %q want %q", got, "bar")
	}
}
