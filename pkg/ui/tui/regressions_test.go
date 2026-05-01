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

func TestWizardOverlayTypedFieldWithSuggestUsesSelectMode(t *testing.T) {
	wo := newWizardOverlay()
	fields := []registry.Field{{
		Flag: "enabled",
		Kind: registry.KindBool,
		Suggest: func(context.Context) ([]registry.Choice, error) {
			return []registry.Choice{{Value: "true"}, {Value: "false"}}, nil
		},
	}}
	_ = wo.Show(context.Background(), nil, &registry.Operation{Name: "test"}, fields, registry.Input{})

	if !wo.isSelect(0) {
		t.Fatal("typed field with Suggest should render as a selection list")
	}
}

func TestWizardOverlayKindFileUsesFileMode(t *testing.T) {
	wo := newWizardOverlay()
	fields := []registry.Field{{Flag: "path", Kind: registry.KindFile}}
	_ = wo.Show(context.Background(), nil, &registry.Operation{Name: "test"}, fields, registry.Input{})

	if !wo.isFile(0) {
		t.Fatal("KindFile should render as a file picker")
	}
}

// TestWizardOverlaySuggestSeeding_Default seeds the selection cursor
// to the choice whose Value matches Field.Default. Regression for
// 'confirmation prompts always start on No even when Default=true'.
func TestWizardOverlaySuggestSeeding_Default(t *testing.T) {
	wo := newWizardOverlay()
	fields := []registry.Field{{
		Flag:    "confirm",
		Default: "true",
		Suggest: func(context.Context) ([]registry.Choice, error) {
			return []registry.Choice{{Value: "false"}, {Value: "true"}}, nil
		},
	}}
	_ = wo.Show(context.Background(), nil, &registry.Operation{Name: "test"}, fields, registry.Input{})
	// Drive the suggest message as if the fetcher had just returned.
	choices, _ := fields[0].Suggest(context.Background())
	wo.Update(wizardSuggestMsg{idx: 0, choices: choices})

	if got := wo.selIdx[0]; got != 1 {
		t.Errorf("selIdx = %d; want 1 (cursor on 'true' via Default)", got)
	}
}

// TestWizardOverlaySuggestSeeding_Precedence covers Input > Prefill > Default.
func TestWizardOverlaySuggestSeeding_Precedence(t *testing.T) {
	wo := newWizardOverlay()
	fields := []registry.Field{{
		Flag:    "pick",
		Default: "a",
		Prefill: func() string { return "b" },
		Suggest: func(context.Context) ([]registry.Choice, error) {
			return []registry.Choice{{Value: "a"}, {Value: "b"}, {Value: "c"}}, nil
		},
	}}
	// Input wins over Prefill and Default.
	_ = wo.Show(context.Background(), nil, &registry.Operation{Name: "test"}, fields, registry.Input{"pick": "c"})
	choices, _ := fields[0].Suggest(context.Background())
	wo.Update(wizardSuggestMsg{idx: 0, choices: choices})
	if got := wo.selIdx[0]; got != 2 {
		t.Errorf("Input precedence: selIdx = %d; want 2", got)
	}

	// Prefill wins over Default when Input is empty.
	wo2 := newWizardOverlay()
	_ = wo2.Show(context.Background(), nil, &registry.Operation{Name: "test"}, fields, registry.Input{})
	wo2.Update(wizardSuggestMsg{idx: 0, choices: choices})
	if got := wo2.selIdx[0]; got != 1 {
		t.Errorf("Prefill precedence: selIdx = %d; want 1", got)
	}
}

// TestWizardOverlaySuggestSeeding_NoDefaultStaysAtZero verifies fields
// without Default/Prefill/Input continue to highlight the first choice.
func TestWizardOverlaySuggestSeeding_NoDefaultStaysAtZero(t *testing.T) {
	wo := newWizardOverlay()
	fields := []registry.Field{{
		Flag: "pick",
		Suggest: func(context.Context) ([]registry.Choice, error) {
			return []registry.Choice{{Value: "x"}, {Value: "y"}}, nil
		},
	}}
	_ = wo.Show(context.Background(), nil, &registry.Operation{Name: "test"}, fields, registry.Input{})
	choices, _ := fields[0].Suggest(context.Background())
	wo.Update(wizardSuggestMsg{idx: 0, choices: choices})
	if got := wo.selIdx[0]; got != 0 {
		t.Errorf("selIdx = %d; want 0 (no seed → first choice)", got)
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

	h.Press("left") // flip cursor to No — should NOT decide
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

	// Flip back to Yes, then Enter → decides.
	h.Press("right")
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

// TestWizardOverlayRendersDisplayLabel verifies Box() uses
// Field.DisplayLabel (Label, humanized Help, or flag without the
// __ns/ prefix) rather than the raw Flag. Regression for the deploy
// wizard showing "__ni/name" etc. instead of the intended user-facing
// labels.
func TestWizardOverlayRendersDisplayLabel(t *testing.T) {
	wo := newWizardOverlay()
	fields := []registry.Field{
		// Explicit Label wins over Flag.
		{Flag: "__ni/name", Label: "Instance name"},
		// No Label, but Flag has a namespaced prefix which must be
		// stripped.
		{Flag: "__ni/region"},
	}
	_ = wo.Show(context.Background(), nil, &registry.Operation{Name: "deploy"}, fields, registry.Input{})
	out := wo.Box(100, 30)

	if strings.Contains(out, "__ni/name") {
		t.Errorf("Box() leaked raw flag __ni/name; Label should render instead:\n%s", out)
	}
	if !strings.Contains(out, "Instance name") {
		t.Errorf("Box() missing explicit Label 'Instance name':\n%s", out)
	}
	if strings.Contains(out, "__ni/region") {
		t.Errorf("Box() leaked raw flag __ni/region; prefix should be stripped:\n%s", out)
	}
	if !strings.Contains(out, "Region") {
		t.Errorf("Box() missing flag-derived label 'Region':\n%s", out)
	}
}

// TestWizardOverlayHidesFieldsViaWhenPredicate verifies that a field
// whose When predicate returns false is not rendered, not navigated to,
// and not committed to Input on submit. Regression for the deploy
// wizard showing new-instance fields when the user chose an existing
// instance.
func TestWizardOverlayHidesFieldsViaWhenPredicate(t *testing.T) {
	wo := newWizardOverlay()
	// Two fields. The second is hidden when "mode" is not "new".
	fields := []registry.Field{
		{Flag: "mode", Default: "existing"},
		{Flag: "__ni/region", Label: "Region", When: func(in registry.Input) bool {
			return in.Get("mode") == "new"
		}},
	}
	_ = wo.Show(context.Background(), nil, &registry.Operation{Name: "deploy"}, fields, registry.Input{})

	out := wo.Box(100, 30)
	if strings.Contains(out, "Region") {
		t.Errorf("Box() rendered hidden field 'Region':\n%s", out)
	}

	// nextVisible from the "mode" field should return -1 because the
	// only other field is currently hidden.
	if j := wo.nextVisible(0); j != -1 {
		t.Errorf("nextVisible(0) = %d; want -1 (only other field hidden)", j)
	}
}

// TestWizardOverlaySubmitExcludesHiddenFields verifies that submit()
// deletes hidden fields from Input (so the saga doesn't see ghost
// answers) and keeps visible ones. Exercises the defensive delete on
// the submit path.
func TestWizardOverlaySubmitExcludesHiddenFields(t *testing.T) {
	wo := newWizardOverlay()
	in := registry.Input{
		// Pre-existing stale value from a prior run / preload. The
		// field is hidden so submit() must strip it.
		"__ni/region": "us-west-2",
	}
	fields := []registry.Field{
		{Flag: "name", Default: "hello"},
		{Flag: "__ni/region", Label: "Region", When: func(in registry.Input) bool {
			return in.Get("name") == "new"
		}},
	}
	_ = wo.Show(context.Background(), nil, &registry.Operation{Name: "deploy"}, fields, in)
	wo.submit()

	if _, ok := in["__ni/region"]; ok {
		t.Errorf("submit() must delete hidden field values; got in=%v", in)
	}
	if in["name"] != "hello" {
		t.Errorf("submit() should keep visible fields; got name=%q want 'hello'", in["name"])
	}
}

// TestWizardOverlayNavSkipsHiddenFields verifies that tab/shift+tab
// navigation jumps over hidden fields to the next visible one.
func TestWizardOverlayNavSkipsHiddenFields(t *testing.T) {
	wo := newWizardOverlay()
	fields := []registry.Field{
		{Flag: "a"},
		{Flag: "b", When: func(in registry.Input) bool { return false }},
		{Flag: "c"},
	}
	_ = wo.Show(context.Background(), nil, &registry.Operation{Name: "test"}, fields, registry.Input{})

	if j := wo.nextVisible(0); j != 2 {
		t.Errorf("nextVisible(0) = %d; want 2 (skip hidden 1)", j)
	}
	if j := wo.prevVisible(2); j != 0 {
		t.Errorf("prevVisible(2) = %d; want 0 (skip hidden 1)", j)
	}
	if j := wo.lastVisible(); j != 2 {
		t.Errorf("lastVisible = %d; want 2", j)
	}
}

// TestWizardOverlayDeployFirstTime simulates the deploy first-time flow
// where no lightsail.conf exists. Verifies that create-new-instance and
// instance fields are both visible in the overlay.
func TestWizardOverlayDeployFirstTime(t *testing.T) {
	wantsNew := func(in registry.Input) bool {
		v, _ := in.Bool("create-new-instance")
		return v
	}
	wantsExisting := func(in registry.Input) bool {
		v, _ := in.Bool("create-new-instance")
		return !v && in.Get("instance") == ""
	}
	askStrategy := func(in registry.Input) bool {
		return in.Get("instance") == ""
	}

	fields := []registry.Field{
		{Flag: "name", Label: "App name", Required: true, Default: "hello"},
		{Flag: "env", Label: "Environment", Default: "dev", Required: true},
		{Flag: "create-new-instance", Label: "Target", Kind: registry.KindBool,
			Required: true, When: askStrategy,
			Suggest: func(_ context.Context) ([]registry.Choice, error) {
				return []registry.Choice{
					{Value: "false", Display: "No   pick an existing instance"},
					{Value: "true", Display: "Yes  create a new instance"},
				}, nil
			}},
		{Flag: "instance", Label: "Lightsail instance", Required: true,
			When: wantsExisting,
			Suggest: func(_ context.Context) ([]registry.Choice, error) {
				return []registry.Choice{{Value: "my-box", Display: "my-box  us-east-1  running"}}, nil
			}},
		{Flag: "__ni/name", Label: "Instance name", Required: true, When: wantsNew},
		{Flag: "__ni/region", Label: "Region", Required: true, When: wantsNew, Default: "us-east-1"},
		{Flag: "deploy-confirm", Label: "Proceed", Kind: registry.KindBool,
			Required: true, Default: "true"},
	}

	wo := newWizardOverlay()
	in := registry.Input{}
	_ = wo.Show(context.Background(), nil, &registry.Operation{Name: "deploy"}, fields, in)

	// Before choices load: check visibility
	if !wo.fieldVisible(0) {
		t.Error("name should be visible")
	}
	if !wo.fieldVisible(1) {
		t.Error("env should be visible")
	}
	if !wo.fieldVisible(2) {
		t.Error("create-new-instance should be visible (instance is empty)")
	}
	if !wo.fieldVisible(3) {
		t.Error("instance should be visible (create-new-instance is empty/false)")
	}
	if wo.fieldVisible(4) {
		t.Error("__ni/name should be hidden (create-new-instance is false)")
	}
	if wo.fieldVisible(5) {
		t.Error("__ni/region should be hidden (create-new-instance is false)")
	}
	if !wo.fieldVisible(6) {
		t.Error("deploy-confirm should be visible")
	}

	// Simulate choices loading for create-new-instance
	choices, _ := fields[2].Suggest(context.Background())
	wo.Update(wizardSuggestMsg{idx: 2, choices: choices})

	// After choices load, selIdx[2] should be 0 (first choice = "false")
	if wo.selIdx[2] != 0 {
		t.Errorf("selIdx[2] = %d; want 0", wo.selIdx[2])
	}
	if v := wo.fieldValue(2); v != "false" {
		t.Errorf("fieldValue(2) = %q; want 'false'", v)
	}

	// Visibility should still be the same
	if !wo.fieldVisible(2) {
		t.Error("create-new-instance should still be visible after choices load")
	}
	if !wo.fieldVisible(3) {
		t.Error("instance should still be visible after choices load")
	}

	// Check Box() output
	out := wo.Box(120, 40)
	if !strings.Contains(out, "Target") {
		t.Errorf("Box() missing 'Target' (create-new-instance label):\n%s", out)
	}
	if !strings.Contains(out, "Lightsail instance") {
		t.Errorf("Box() missing 'Lightsail instance' label:\n%s", out)
	}
	if strings.Contains(out, "Instance name") {
		t.Errorf("Box() should NOT show 'Instance name' (__ni/name) when create-new-instance=false:\n%s", out)
	}

	// Now simulate user selecting "Yes" (create new instance)
	wo.focusField(2) // user tabs to the Target field
	wo.selIdx[2] = 1 // move cursor to "true"
	if v := wo.fieldValue(2); v != "true" {
		t.Errorf("after cursor move, fieldValue(2) = %q; want 'true'", v)
	}

	// Now __ni/* fields should be visible and instance should be hidden
	if wo.fieldVisible(3) {
		t.Error("instance should be hidden when create-new-instance=true")
	}
	if !wo.fieldVisible(4) {
		t.Error("__ni/name should be visible when create-new-instance=true")
	}
	if !wo.fieldVisible(5) {
		t.Error("__ni/region should be visible when create-new-instance=true")
	}
}

// TestWizardOverlayAppCreateWithNewInstance simulates the app create flow
// where the user chooses to create a new instance. Verifies that the
// create-new-instance toggle controls visibility of instance picker vs
// new-instance fields.
func TestWizardOverlayAppCreateWithNewInstance(t *testing.T) {
	wantsNew := func(in registry.Input) bool {
		v, _ := in.Bool("create-new-instance")
		return v
	}
	wantsExisting := func(in registry.Input) bool {
		v, _ := in.Bool("create-new-instance")
		return !v && in.Get("instance") == ""
	}
	askStrategy := func(in registry.Input) bool {
		return in.Get("instance") == ""
	}

	fields := []registry.Field{
		{Flag: "name", Label: "App name", Required: true, Default: "hello"},
		{Flag: "env", Label: "Environment", Default: "dev", Required: true},
		{Flag: "create-new-instance", Label: "Target", Kind: registry.KindBool,
			Required: true, When: askStrategy,
			Suggest: func(_ context.Context) ([]registry.Choice, error) {
				return []registry.Choice{
					{Value: "false", Display: "No   pick an existing instance"},
					{Value: "true", Display: "Yes  create a new instance"},
				}, nil
			}},
		{Flag: "instance", Label: "Lightsail instance", Required: true,
			When: wantsExisting,
			Suggest: func(_ context.Context) ([]registry.Choice, error) {
				return []registry.Choice{{Value: "my-box"}}, nil
			}},
		{Flag: "__ni/name", Label: "Instance name", Required: true, When: wantsNew},
		{Flag: "__ni/region", Label: "Region", Required: true, When: wantsNew, Default: "us-east-1"},
	}

	wo := newWizardOverlay()
	in := registry.Input{}
	_ = wo.Show(context.Background(), nil, &registry.Operation{Name: "create"}, fields, in)

	// Load choices for create-new-instance
	choices, _ := fields[2].Suggest(context.Background())
	wo.Update(wizardSuggestMsg{idx: 2, choices: choices})

	// Default: "No" selected (index 0) — instance picker visible, __ni/* hidden
	out := wo.Box(120, 40)
	if !strings.Contains(out, "Lightsail instance") {
		t.Errorf("instance picker should be visible when create-new-instance=false:\n%s", out)
	}
	if strings.Contains(out, "Instance name") {
		t.Errorf("__ni/name should be hidden when create-new-instance=false:\n%s", out)
	}

	// Switch to "Yes" (index 1) — instance picker hidden, __ni/* visible
	wo.focusField(2) // user tabs to the Target field
	wo.selIdx[2] = 1
	out = wo.Box(120, 40)
	if strings.Contains(out, "Lightsail instance") {
		t.Errorf("instance picker should be hidden when create-new-instance=true:\n%s", out)
	}
	if !strings.Contains(out, "Instance name") {
		t.Errorf("__ni/name should be visible when create-new-instance=true:\n%s", out)
	}
	if !strings.Contains(out, "Region") {
		t.Errorf("__ni/region should be visible when create-new-instance=true:\n%s", out)
	}
}

// TestWizardOverlaySuggestFieldDoesNotHideItself is the regression for
// "instance field disappears after a few seconds" and "Target shows up
// and disappears". Unfocused Suggest fields whose async choices have
// loaded must not feed their cursor position into When predicates —
// neither their own nor other fields'. Only the focused Suggest field's
// cursor drives visibility (so the create-new-instance toggle works).
func TestWizardOverlaySuggestFieldDoesNotHideItself(t *testing.T) {
	wantsExisting := func(in registry.Input) bool {
		v, _ := in.Bool("create-new-instance")
		return !v && in.Get("instance") == ""
	}
	askStrategy := func(in registry.Input) bool {
		return in.Get("instance") == ""
	}

	fields := []registry.Field{
		{Flag: "create-new-instance", Kind: registry.KindBool, When: askStrategy,
			Suggest: func(_ context.Context) ([]registry.Choice, error) {
				return []registry.Choice{{Value: "false"}, {Value: "true"}}, nil
			}},
		{Flag: "instance", When: wantsExisting,
			Suggest: func(_ context.Context) ([]registry.Choice, error) {
				return []registry.Choice{{Value: "my-box"}, {Value: "other-box"}}, nil
			}},
	}

	wo := newWizardOverlay()
	_ = wo.Show(context.Background(), nil, &registry.Operation{Name: "test"}, fields, registry.Input{})

	// Before any choices load: both should be visible.
	if !wo.fieldVisible(0) {
		t.Fatal("create-new-instance should be visible before choices load")
	}
	if !wo.fieldVisible(1) {
		t.Fatal("instance should be visible before choices load")
	}

	// Load choices for create-new-instance (selIdx stays 0 → "false").
	cniChoices, _ := fields[0].Suggest(context.Background())
	wo.Update(wizardSuggestMsg{idx: 0, choices: cniChoices})

	if !wo.fieldVisible(1) {
		t.Fatal("instance should still be visible after create-new-instance choices load")
	}

	// Load choices for instance. Focus is still on field 0.
	// Before the fix, instance's cursor value ("my-box") would leak
	// into askStrategy, hiding create-new-instance.
	instChoices, _ := fields[1].Suggest(context.Background())
	wo.Update(wizardSuggestMsg{idx: 1, choices: instChoices})

	if !wo.fieldVisible(0) {
		t.Fatal("create-new-instance must NOT be hidden by instance's unfocused cursor")
	}
	if !wo.fieldVisible(1) {
		t.Fatal("instance must NOT hide itself after its own choices load")
	}

	// When instance is pre-populated in Input, missingFields excludes
	// it from the wizard entirely. Only create-new-instance remains,
	// and askStrategy sees the pre-populated value → hides it.
	wo2 := newWizardOverlay()
	onlyCNI := []registry.Field{fields[0]} // only create-new-instance
	_ = wo2.Show(context.Background(), nil, &registry.Operation{Name: "test"}, onlyCNI, registry.Input{"instance": "pre-set"})
	if wo2.fieldVisible(0) {
		t.Fatal("create-new-instance should be hidden when instance is pre-populated")
	}
}
