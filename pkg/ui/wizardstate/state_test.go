package wizardstate

import (
	"context"
	"testing"

	"github.com/bwagner5/triad/pkg/registry"
)

// makeChoices returns a Suggest fn that yields fixed choices.
func makeChoices(vs ...string) func(context.Context) ([]registry.Choice, error) {
	return func(context.Context) ([]registry.Choice, error) {
		out := make([]registry.Choice, len(vs))
		for i, v := range vs {
			out[i] = registry.Choice{Value: v}
		}
		return out, nil
	}
}

func TestNewSeedsCommittedFromInput(t *testing.T) {
	fields := []registry.Field{{Flag: "name"}, {Flag: "size"}}
	s := New(fields, registry.Input{"name": "alice"})

	if !s.Entry(0).Committed {
		t.Errorf("entry 0 should be Committed (pre-seeded from in)")
	}
	if s.Entry(0).Text != "alice" {
		t.Errorf("entry 0 Text = %q, want %q", s.Entry(0).Text, "alice")
	}
	if s.Entry(1).Committed {
		t.Errorf("entry 1 should NOT be Committed (no in value)")
	}
}

func TestNewSeedsTextFromPrefillThenDefault(t *testing.T) {
	prefilled := []registry.Field{
		{Flag: "a", Prefill: func() string { return "from-prefill" }},
		{Flag: "b", Default: "from-default"},
		{Flag: "c", Prefill: func() string { return "p" }, Default: "d"}, // prefill wins
	}
	s := New(prefilled, registry.Input{})

	if s.Entry(0).Text != "from-prefill" {
		t.Errorf("entry 0 Text = %q, want from-prefill", s.Entry(0).Text)
	}
	if s.Entry(1).Text != "from-default" {
		t.Errorf("entry 1 Text = %q, want from-default", s.Entry(1).Text)
	}
	if s.Entry(2).Text != "p" {
		t.Errorf("entry 2 Text = %q, want p (prefill wins over default)", s.Entry(2).Text)
	}
	for i := 0; i < 3; i++ {
		if s.Entry(i).Committed {
			t.Errorf("entry %d should not be Committed (no in value)", i)
		}
	}
}

func TestSetChoicesSeedsCursorFromInput(t *testing.T) {
	fields := []registry.Field{{Flag: "size", Suggest: makeChoices("small", "medium", "large")}}
	s := New(fields, registry.Input{"size": "medium"})

	sel := s.SetChoices(0, []registry.Choice{
		{Value: "small"}, {Value: "medium"}, {Value: "large"},
	})
	if sel != 1 {
		t.Errorf("SetChoices returned %d, want 1 (medium)", sel)
	}
}

func TestSetChoicesSeedsCursorFromPrefill(t *testing.T) {
	fields := []registry.Field{{
		Flag:    "size",
		Suggest: makeChoices("small", "medium", "large"),
		Prefill: func() string { return "large" },
	}}
	s := New(fields, registry.Input{})
	sel := s.SetChoices(0, []registry.Choice{
		{Value: "small"}, {Value: "medium"}, {Value: "large"},
	})
	if sel != 2 {
		t.Errorf("SetChoices returned %d, want 2 (large)", sel)
	}
}

func TestSetChoicesSeedsCursorFromDefault(t *testing.T) {
	fields := []registry.Field{{
		Flag:    "size",
		Suggest: makeChoices("small", "medium", "large"),
		Default: "small",
	}}
	s := New(fields, registry.Input{})
	sel := s.SetChoices(0, []registry.Choice{
		{Value: "small"}, {Value: "medium"}, {Value: "large"},
	})
	if sel != 0 {
		t.Errorf("SetChoices returned %d, want 0 (small)", sel)
	}
}

func TestSetChoicesSeedsMultiSelFromInput(t *testing.T) {
	fields := []registry.Field{{
		Flag:    "tags",
		Suggest: makeChoices("a", "b", "c"),
		Multi:   true,
	}}
	s := New(fields, registry.Input{"tags": "a,c"})
	s.SetChoices(0, []registry.Choice{{Value: "a"}, {Value: "b"}, {Value: "c"}})

	got := s.Entry(0).MultiSel
	if !got[0] || got[1] || !got[2] {
		t.Errorf("MultiSel = %v, want {0:true, 2:true}", got)
	}
	if v := s.Value(0); v != "a,c" {
		t.Errorf("Value = %q, want a,c", v)
	}
}

func TestFieldVisibleEvaluatesWhen(t *testing.T) {
	fields := []registry.Field{
		{Flag: "create-new", Default: "false"},
		{Flag: "name", When: func(in registry.Input) bool { return in["create-new"] == "true" }},
	}
	// Without seeding, default text "false" is in entry 0, so When returns false.
	s := New(fields, registry.Input{})
	if s.FieldVisible(1) {
		t.Errorf("field 1 should be hidden when create-new=false")
	}
	s.SetText(0, "true")
	if !s.FieldVisible(1) {
		t.Errorf("field 1 should be visible when create-new=true")
	}
}

func TestNextPrevVisibleSkipHidden(t *testing.T) {
	fields := []registry.Field{
		{Flag: "a"},
		{Flag: "b", When: func(in registry.Input) bool { return false }},
		{Flag: "c"},
	}
	s := New(fields, registry.Input{})
	if got := s.NextVisible(0); got != 2 {
		t.Errorf("NextVisible(0) = %d, want 2", got)
	}
	if got := s.PrevVisible(2); got != 0 {
		t.Errorf("PrevVisible(2) = %d, want 0", got)
	}
	if got := s.FirstVisible(); got != 0 {
		t.Errorf("FirstVisible = %d, want 0", got)
	}
	if got := s.LastVisible(); got != 2 {
		t.Errorf("LastVisible = %d, want 2", got)
	}
}

func TestCommitClearsHiddenDownstream(t *testing.T) {
	fields := []registry.Field{
		{Flag: "create-new"},
		{Flag: "name", When: func(in registry.Input) bool { return in["create-new"] == "true" }},
	}
	s := New(fields, registry.Input{})
	if err := s.CommitValue(0, "true"); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitValue(1, "alice"); err != nil {
		t.Fatal(err)
	}
	if s.Entry(1).Text != "alice" {
		t.Fatalf("setup: entry 1 should hold alice, got %q", s.Entry(1).Text)
	}

	// Flip create-new to false; the downstream answer should be cleared.
	if err := s.CommitValue(0, "false"); err != nil {
		t.Fatal(err)
	}
	if s.Entry(1).Text != "" {
		t.Errorf("entry 1 Text should be cleared after When flips, got %q", s.Entry(1).Text)
	}
	if s.Entry(1).Committed {
		t.Errorf("entry 1 should no longer be Committed after hide")
	}
}

func TestGoBackPreservesText(t *testing.T) {
	fields := []registry.Field{{Flag: "a"}, {Flag: "b"}}
	s := New(fields, registry.Input{})
	if err := s.CommitValue(0, "foo"); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitValue(1, "bar"); err != nil {
		t.Fatal(err)
	}
	target := s.GoBack(1)
	if target != 0 {
		t.Fatalf("GoBack(1) = %d, want 0", target)
	}
	// THE BUG FIX: the user's previous answer must survive shift+tab so
	// they can edit it instead of retype from scratch.
	if got := s.Entry(0).Text; got != "foo" {
		t.Errorf("entry 0 Text after GoBack = %q, want foo (shift+tab must preserve typed value)", got)
	}
	if s.Entry(0).Committed {
		t.Errorf("entry 0 should be uncommitted so it gets re-prompted")
	}
	// Downstream answer also survives unless the user changes earlier
	// answers in a way that flips its When.
	if got := s.Entry(1).Text; got != "bar" {
		t.Errorf("entry 1 Text after GoBack = %q, want bar (downstream survives)", got)
	}
}

func TestGoBackPreservesSelIdxAndMultiSel(t *testing.T) {
	fields := []registry.Field{
		{Flag: "size", Suggest: makeChoices("s", "m", "l")},
		{Flag: "tags", Suggest: makeChoices("a", "b", "c"), Multi: true},
	}
	s := New(fields, registry.Input{})
	s.SetChoices(0, []registry.Choice{{Value: "s"}, {Value: "m"}, {Value: "l"}})
	s.SetSelIdx(0, 2) // pick "l"
	if err := s.Commit(0); err != nil {
		t.Fatal(err)
	}
	s.SetChoices(1, []registry.Choice{{Value: "a"}, {Value: "b"}, {Value: "c"}})
	s.ToggleMulti(1, 0)
	s.ToggleMulti(1, 2)
	if err := s.Commit(1); err != nil {
		t.Fatal(err)
	}

	if target := s.GoBack(1); target != 0 {
		t.Fatalf("GoBack returned %d, want 0", target)
	}
	if s.Entry(0).SelIdx != 2 {
		t.Errorf("SelIdx = %d, want 2 (preserved)", s.Entry(0).SelIdx)
	}
	got := s.Entry(1).MultiSel
	if !got[0] || got[1] || !got[2] {
		t.Errorf("MultiSel = %v, want {0:true, 2:true} (preserved)", got)
	}
}

func TestGoBackSkipsHiddenCommitted(t *testing.T) {
	// A previously-committed field that has since become hidden should
	// not be a valid GoBack target. (Realistically the cascading-clear
	// would have cleared it, so Committed would already be false — this
	// test is defensive.)
	fields := []registry.Field{
		{Flag: "a"},
		{Flag: "b", When: func(in registry.Input) bool { return in["a"] == "yes" }},
		{Flag: "c"},
	}
	s := New(fields, registry.Input{})
	if err := s.CommitValue(0, "yes"); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitValue(1, "x"); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitValue(2, "y"); err != nil {
		t.Fatal(err)
	}
	// Flip a to "no" — b becomes hidden, its slot is cleared.
	if err := s.CommitValue(0, "no"); err != nil {
		t.Fatal(err)
	}
	if s.Entry(1).Committed {
		t.Fatalf("setup: entry 1 should be uncommitted after hide")
	}
	// Now from c (idx 2), GoBack should land on a (idx 0), skipping b.
	if target := s.GoBack(2); target != 0 {
		t.Errorf("GoBack(2) = %d, want 0 (skip hidden b)", target)
	}
}

func TestSubmitFlagsFirstInvalid(t *testing.T) {
	fields := []registry.Field{
		{Flag: "a", Required: true},
		{Flag: "b", Required: true},
	}
	s := New(fields, registry.Input{})
	s.SetText(1, "hello")
	if got := s.Submit(); got != 0 {
		t.Errorf("Submit = %d, want 0 (first required-empty)", got)
	}
	if s.Entry(0).Err != "required" {
		t.Errorf("entry 0 Err = %q, want required", s.Entry(0).Err)
	}
	s.SetText(0, "filled")
	if got := s.Submit(); got != -1 {
		t.Errorf("Submit = %d, want -1 (all valid)", got)
	}
}

func TestApplyToInputDropsHiddenFlags(t *testing.T) {
	fields := []registry.Field{
		{Flag: "create-new"},
		{Flag: "name", When: func(in registry.Input) bool { return in["create-new"] == "true" }},
	}
	in := registry.Input{"name": "ghost"} // pre-existing stale value
	s := New(fields, in)
	s.SetText(0, "false") // hides "name"

	out := registry.Input{"name": "ghost"}
	s.ApplyToInput(out)
	if _, ok := out["name"]; ok {
		t.Errorf("ApplyToInput should delete hidden flag 'name', got %v", out)
	}
}

func TestLiveInputForWhenExcludesSelf(t *testing.T) {
	fields := []registry.Field{{Flag: "a"}, {Flag: "b"}}
	s := New(fields, registry.Input{"a": "1", "b": "2"})
	got := s.LiveInputForWhen(1)
	if got["b"] != "" {
		t.Errorf("LiveInputForWhen(1) should not include b, got %v", got)
	}
	if got["a"] != "1" {
		t.Errorf("LiveInputForWhen(1) should include a=1, got %v", got)
	}
}

func TestLiveInputForWhenIgnoresLaterSuggestCursor(t *testing.T) {
	// A Suggest field at idx 2 should not contribute to the When
	// snapshot for idx 1 — that would let a later cursor hide an
	// earlier field and break shift+tab navigation.
	fields := []registry.Field{
		{Flag: "a"},
		{Flag: "b"},
		{Flag: "c", Suggest: makeChoices("x", "y")},
	}
	s := New(fields, registry.Input{})
	s.SetChoices(2, []registry.Choice{{Value: "x"}, {Value: "y"}})
	s.SetSelIdx(2, 1)

	got := s.LiveInputForWhen(1)
	if _, ok := got["c"]; ok {
		t.Errorf("LiveInputForWhen(1) should exclude later Suggest c, got %v", got)
	}
}

func TestCommittedHistoryMasksSensitive(t *testing.T) {
	fields := []registry.Field{
		{Flag: "user"},
		{Flag: "password", Sensitive: true},
	}
	s := New(fields, registry.Input{})
	if err := s.CommitValue(0, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitValue(1, "secret"); err != nil {
		t.Fatal(err)
	}
	hist := s.CommittedHistory()
	if len(hist) != 2 {
		t.Fatalf("got %d history entries, want 2", len(hist))
	}
	if hist[0].DisplayVal != "alice" {
		t.Errorf("user DisplayVal = %q, want alice", hist[0].DisplayVal)
	}
	if hist[1].DisplayVal != "••••••" {
		t.Errorf("password DisplayVal = %q, want six bullets", hist[1].DisplayVal)
	}
}

func TestSkipOptionalRejectsRequired(t *testing.T) {
	fields := []registry.Field{{Flag: "a", Required: true}}
	s := New(fields, registry.Input{})
	if err := s.SkipOptional(0); err == nil {
		t.Errorf("SkipOptional on required field should error")
	}
}

func TestSkipOptionalCommitsEmpty(t *testing.T) {
	fields := []registry.Field{{Flag: "a"}}
	s := New(fields, registry.Input{})
	s.SetText(0, "stale")
	if err := s.SkipOptional(0); err != nil {
		t.Fatal(err)
	}
	if !s.Entry(0).Committed {
		t.Errorf("SkipOptional should mark Committed=true")
	}
	if s.Entry(0).Text != "" {
		t.Errorf("SkipOptional should clear Text, got %q", s.Entry(0).Text)
	}
}

func TestCommitValidationError(t *testing.T) {
	fields := []registry.Field{{
		Flag: "n", Kind: registry.KindBool,
	}}
	s := New(fields, registry.Input{})
	err := s.CommitValue(0, "not-a-bool")
	if err == nil {
		t.Fatalf("expected validation error")
	}
	if s.Entry(0).Committed {
		t.Errorf("entry should not be Committed after validation failure")
	}
	if s.Entry(0).Err == "" {
		t.Errorf("entry Err should be populated after validation failure")
	}
}

func TestValueResolvesEachKind(t *testing.T) {
	fields := []registry.Field{
		{Flag: "t"},
		{Flag: "f", Kind: registry.KindFile},
		{Flag: "s", Suggest: makeChoices("a", "b")},
		{Flag: "m", Suggest: makeChoices("x", "y", "z"), Multi: true},
	}
	s := New(fields, registry.Input{})
	s.SetText(0, "txt")
	s.SetFilePath(1, "/tmp/f")
	s.SetChoices(2, []registry.Choice{{Value: "a"}, {Value: "b"}})
	s.SetSelIdx(2, 1)
	s.SetChoices(3, []registry.Choice{{Value: "x"}, {Value: "y"}, {Value: "z"}})
	s.ToggleMulti(3, 0)
	s.ToggleMulti(3, 2)

	cases := []struct{ idx int; want string }{
		{0, "txt"},
		{1, "/tmp/f"},
		{2, "b"},
		{3, "x,z"},
	}
	for _, c := range cases {
		if got := s.Value(c.idx); got != c.want {
			t.Errorf("Value(%d) = %q, want %q", c.idx, got, c.want)
		}
	}
}
