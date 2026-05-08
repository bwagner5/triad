package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/bwagner5/triad/pkg/registry"
	"github.com/bwagner5/triad/pkg/runtime"
)

// TestTabsOnLastRow asserts the k9s-style breadcrumb pills live on the bottom row.
func TestTabsOnLastRow(t *testing.T) {
	reg := registry.New()
	a := newApp(context.Background(), reg, Options{Name: "test"})
	// Register a fake resource so there's at least one pill.
	reg.Register(registry.Resource{Name: "thing", Plural: "things", Store: fakeStore{}})
	a.resource = ptr(reg.All()[0])
	var m tea.Model = a
	m, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	plain := stripANSI(m.(*app).View().Content)
	lines := strings.Split(plain, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) < 30 {
		t.Fatalf("expected >= 30 lines, got %d", len(lines))
	}
	last := lines[len(lines)-1]
	if !strings.Contains(last, "<thing>") {
		t.Errorf("last line missing pill: %q", last)
	}
}

type fakeStore struct{}

func (fakeStore) Get(_ context.Context, _ string) (any, error) { return nil, nil }
func (fakeStore) List(_ context.Context, _ registry.Filter) ([]any, error) {
	return nil, nil
}
func ptr[T any](v T) *T { return &v }

// TestEmptyTableHidesRowDependentHints asserts the bottom status bar
// sheds row-dependent hints (delete/logs-style ops, filter) when the
// table has no rows — the bar should only surface actions that work
// without a selection (refresh, global ops, palette, help, quit). The
// hidden bindings MUST still appear in the "?" help overlay so users
// can discover them and the keys remain dispatchable.
func TestEmptyTableHidesRowDependentHints(t *testing.T) {
	reg := registry.New()
	// Resource with a primary-key field and two ops:
	//  - "delete" needs a selected row (Suggest on the PK flag).
	//  - "create" does not (HideFromStatusBar is already implied by
	//    needsSelection returning false; we leave it visible to
	//    confirm that non-row ops aren't swept up by the new rule).
	reg.Register(registry.Resource{
		Name: "thing", Plural: "things", Store: fakeStore{},
		Fields: []registry.Field{
			{Name: "Name", Flag: "name", Table: registry.TableHint{Header: "NAME"}},
		},
		Operations: map[string]registry.Operation{
			"delete": {
				Name: "delete", Key: "ctrl+d",
				Fields: []registry.Field{
					{Flag: "name", Required: true, Suggest: func(context.Context) ([]registry.Choice, error) { return nil, nil }},
				},
				Steps: []registry.Step{{Label: "n", Do: func(context.Context, *registry.State) error { return nil }}},
			},
		},
	})
	a := newApp(context.Background(), reg, Options{Name: "test"})
	a.resource = ptr(reg.All()[0])
	// Explicit: no items, no filter — the "empty table" state.
	a.items = nil
	a.filterText = ""

	var m tea.Model = a
	m, _ = m.Update(tea.WindowSizeMsg{Width: 160, Height: 30})

	plain := stripANSI(m.(*app).View().Content)
	lines := strings.Split(plain, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	last := lines[len(lines)-1]

	// Must NOT advertise row-dependent hints on the bar.
	for _, hidden := range []string{"delete", "filter"} {
		if strings.Contains(last, hidden) {
			t.Errorf("bottom bar should hide %q when table is empty:\n%s", hidden, last)
		}
	}
	// Must still advertise the always-available actions.
	for _, shown := range []string{"refresh", "palette", "help", "quit"} {
		if !strings.Contains(last, shown) {
			t.Errorf("bottom bar should keep %q when table is empty:\n%s", shown, last)
		}
	}

	// Opening "?" must still list the hidden bindings so users can
	// discover them.
	m, _ = m.Update(tea.KeyPressMsg{Code: '?', Text: "?"})
	withHelp := stripANSI(m.(*app).View().Content)
	for _, label := range []string{"delete", "filter"} {
		if !strings.Contains(withHelp, label) {
			t.Errorf("help overlay missing %q:\n%s", label, withHelp)
		}
	}
}

// TestPopulatedTableShowsRowDependentHints is the happy-path counterpart
// to TestEmptyTableHidesRowDependentHints: once items are present the
// bar restores the row-dependent hints.
func TestPopulatedTableShowsRowDependentHints(t *testing.T) {
	type row struct{ Name string }
	reg := registry.New()
	reg.Register(registry.Resource{
		Name: "thing", Plural: "things", Store: fakeStore{},
		Fields: []registry.Field{
			{Name: "Name", Flag: "name", Table: registry.TableHint{Header: "NAME"}},
		},
		Operations: map[string]registry.Operation{
			"delete": {
				Name: "delete", Key: "ctrl+d",
				Fields: []registry.Field{
					{Flag: "name", Required: true, Suggest: func(context.Context) ([]registry.Choice, error) { return nil, nil }},
				},
				Steps: []registry.Step{{Label: "n", Do: func(context.Context, *registry.State) error { return nil }}},
			},
		},
	})
	a := newApp(context.Background(), reg, Options{Name: "test"})
	a.resource = ptr(reg.All()[0])
	a.items = []any{row{Name: "a"}}
	a.cursor = 0

	var m tea.Model = a
	m, _ = m.Update(tea.WindowSizeMsg{Width: 160, Height: 30})

	plain := stripANSI(m.(*app).View().Content)
	lines := strings.Split(plain, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	last := lines[len(lines)-1]

	for _, shown := range []string{"delete", "filter", "refresh", "palette", "help", "quit"} {
		if !strings.Contains(last, shown) {
			t.Errorf("bottom bar should show %q when table has rows:\n%s", shown, last)
		}
	}
}

// TestNeedsExistingRowFlagHidesFromBarWhenEmpty pins the explicit
// registry.Operation.NeedsExistingRow opt-in: ops that require an
// existing row (but pick up their PK from Prefill / config instead
// of Suggest) must still be hidden from the bottom bar when the
// table is empty, and must still be listed in the "?" help overlay.
//
// Regression guard for the add-target case where "<t>" was leaking
// onto the status bar with an empty apps list because the op's
// name field uses Prefill, not Suggest, and the auto-detection
// heuristic missed it.
func TestNeedsExistingRowFlagHidesFromBarWhenEmpty(t *testing.T) {
	reg := registry.New()
	reg.Register(registry.Resource{
		Name: "app", Plural: "apps", Store: fakeStore{},
		Fields: []registry.Field{
			{Name: "Name", Flag: "name", Table: registry.TableHint{Header: "NAME"}},
		},
		Operations: map[string]registry.Operation{
			"add-target": {
				Name: "add-target", Key: "t",
				NeedsExistingRow: true,
				// name has Prefill but NO Suggest — this is the
				// combination the old heuristic missed.
				Fields: []registry.Field{
					{Flag: "name", Required: true, Prefill: func() string { return "" }},
				},
				Steps: []registry.Step{{Label: "n", Do: func(context.Context, *registry.State) error { return nil }}},
			},
		},
	})
	a := newApp(context.Background(), reg, Options{Name: "test"})
	a.resource = ptr(reg.All()[0])
	// Empty table.
	a.items = nil
	a.filterText = ""

	var m tea.Model = a
	m, _ = m.Update(tea.WindowSizeMsg{Width: 160, Height: 30})

	plain := stripANSI(m.(*app).View().Content)
	lines := strings.Split(plain, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	last := lines[len(lines)-1]
	if strings.Contains(last, "add-target") {
		t.Errorf("bottom bar should hide add-target when table is empty:\n%s", last)
	}

	// Still discoverable via "?" help.
	m, _ = m.Update(tea.KeyPressMsg{Code: '?', Text: "?"})
	withHelp := stripANSI(m.(*app).View().Content)
	if !strings.Contains(withHelp, "add-target") {
		t.Errorf("help overlay missing add-target:\n%s", withHelp)
	}
}

// TestNeedsExistingRowFlagShowsOnPopulatedTable confirms the flag
// only gates the bar on emptiness — once there's a row, the hint
// must come back.
func TestNeedsExistingRowFlagShowsOnPopulatedTable(t *testing.T) {
	type row struct{ Name string }
	reg := registry.New()
	reg.Register(registry.Resource{
		Name: "app", Plural: "apps", Store: fakeStore{},
		Fields: []registry.Field{
			{Name: "Name", Flag: "name", Table: registry.TableHint{Header: "NAME"}},
		},
		Operations: map[string]registry.Operation{
			"add-target": {
				Name: "add-target", Key: "t",
				NeedsExistingRow: true,
				Fields: []registry.Field{
					{Flag: "name", Required: true, Prefill: func() string { return "" }},
				},
				Steps: []registry.Step{{Label: "n", Do: func(context.Context, *registry.State) error { return nil }}},
			},
		},
	})
	a := newApp(context.Background(), reg, Options{Name: "test"})
	a.resource = ptr(reg.All()[0])
	a.items = []any{row{Name: "my-app"}}
	a.cursor = 0

	var m tea.Model = a
	m, _ = m.Update(tea.WindowSizeMsg{Width: 160, Height: 30})

	plain := stripANSI(m.(*app).View().Content)
	lines := strings.Split(plain, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	last := lines[len(lines)-1]
	if !strings.Contains(last, "add-target") {
		t.Errorf("bottom bar should show add-target when a row is present:\n%s", last)
	}
}

// TestHideFromStatusBar_RemovesFromBarButKeepsInHelp is a regression
// guard: an operation that opted into HideFromStatusBar must NOT
// appear in the bottom status-bar hint row, but MUST still appear in
// the "?" help overlay (and remain key-dispatchable + palette-listed).
func TestHideFromStatusBar_RemovesFromBarButKeepsInHelp(t *testing.T) {
	reg := registry.New()
	reg.Register(registry.Resource{
		Name: "thing", Plural: "things", Store: fakeStore{},
		Operations: map[string]registry.Operation{
			"create": {
				Name: "create", Key: "c",
				HideFromStatusBar: true,
				Steps:             []registry.Step{{Label: "n", Do: func(context.Context, *registry.State) error { return nil }}},
			},
			"deploy": {
				Name: "deploy", Key: "d",
				Steps: []registry.Step{{Label: "n", Do: func(context.Context, *registry.State) error { return nil }}},
			},
		},
	})
	a := newApp(context.Background(), reg, Options{Name: "test"})
	a.resource = ptr(reg.All()[0])
	var m tea.Model = a
	m, _ = m.Update(tea.WindowSizeMsg{Width: 160, Height: 30})

	// Bottom status bar: visible ops only.
	plain := stripANSI(m.(*app).View().Content)
	lines := strings.Split(plain, "\n")
	if len(lines) == 0 {
		t.Fatal("empty render")
	}
	last := lines[len(lines)-1]
	if strings.Contains(last, "create") {
		t.Errorf("bottom bar should NOT contain 'create':\n%s", last)
	}
	if !strings.Contains(last, "deploy") {
		t.Errorf("bottom bar should contain 'deploy':\n%s", last)
	}

	// "?" help overlay: must list every binding, including hidden ones.
	m, _ = m.Update(tea.KeyPressMsg{Code: '?', Text: "?"})
	withHelp := stripANSI(m.(*app).View().Content)
	if !strings.Contains(withHelp, "create") {
		t.Errorf("help overlay missing 'create':\n%s", withHelp)
	}
	if !strings.Contains(withHelp, "deploy") {
		t.Errorf("help overlay missing 'deploy':\n%s", withHelp)
	}
}

// TestHelpOverlayCentered asserts the help modal is roughly centered in both
// axes when toggled.
func TestHelpOverlayCentered(t *testing.T) {
	reg := registry.New()
	a := newApp(context.Background(), reg, Options{Name: "test"})
	var m tea.Model = a
	m, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	// Simulate "?" press.
	m, _ = m.Update(tea.KeyPressMsg{Code: '?', Text: "?"})
	v := m.(*app).View()
	plain := stripANSI(v.Content)
	lines := strings.Split(plain, "\n")
	if len(lines) < 24 {
		t.Fatalf("expected >= 24 lines, got %d", len(lines))
	}
	// Find the line containing "Help" title.
	row := -1
	for i, ln := range lines {
		if strings.Contains(ln, "Help") {
			row = i
			break
		}
	}
	if row < 0 {
		t.Fatalf("Help modal not rendered\n%s", plain)
	}
	// The modal should be vertically near the center (row between 4 and 19 of 24).
	if row < 4 || row > 19 {
		t.Errorf("Help modal not vertically centered: row=%d\n%s", row, plain)
	}
	// And horizontally: the "Help" heading should not start at column 0.
	col := strings.Index(lines[row], "Help")
	if col < 5 {
		t.Errorf("Help modal not horizontally centered: col=%d, line=%q", col, lines[row])
	}
}

// ---- §8 additions ----

func TestFooterShowsVersion(t *testing.T) {
	reg := registry.New()
	reg.Register(registry.Resource{Name: "thing", Plural: "things", Store: fakeStore{}})
	a := newApp(context.Background(), reg, Options{Name: "test", Version: "v1.2.3"})
	a.resource = ptr(reg.All()[0])
	var m tea.Model = a
	m, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	plain := stripANSI(m.(*app).View().Content)
	lines := strings.Split(plain, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	last := lines[len(lines)-1]
	if !strings.Contains(last, "v1.2.3") {
		t.Errorf("footer missing version:\n%q", last)
	}
}

func TestFooterEmptyVersionOK(t *testing.T) {
	reg := registry.New()
	reg.Register(registry.Resource{Name: "thing", Plural: "things", Store: fakeStore{}})
	a := newApp(context.Background(), reg, Options{Name: "test"})
	a.resource = ptr(reg.All()[0])
	var m tea.Model = a
	// Should not panic and render width-consistent footer.
	m, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	_ = m.(*app).View()
}

func TestNumberKeySwitchesResource(t *testing.T) {
	reg := registry.New()
	reg.Register(registry.Resource{Name: "alpha", Plural: "alphas", Store: fakeStore{}})
	reg.Register(registry.Resource{Name: "beta", Plural: "betas", Store: fakeStore{}})
	a := newApp(context.Background(), reg, Options{Name: "test"})
	a.resource = ptr(reg.All()[1]) // beta
	var m tea.Model = a
	m, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	// "1" selects the first registered resource (alpha). Previously
	// "1" selected index 1 (beta); we want 1-based keys so the
	// number row lines up with how users read the breadcrumb pills
	// left-to-right.
	m, _ = m.Update(tea.KeyPressMsg{Code: '1', Text: "1"})
	if got := m.(*app).resource.Name; got != "alpha" {
		t.Errorf("after '1', resource = %q, want alpha", got)
	}
	// "2" advances to the second registered resource.
	m, _ = m.Update(tea.KeyPressMsg{Code: '2', Text: "2"})
	if got := m.(*app).resource.Name; got != "beta" {
		t.Errorf("after '2', resource = %q, want beta", got)
	}
}

// TestZeroKeySelectsTenthResource pins the one non-obvious piece of
// the 1-based mapping: "0" sits on the far right of the number row
// and conventionally means "10" when numeric keys are used as
// sequential selectors (browser tabs, IDEs, tmux window switching).
func TestZeroKeySelectsTenthResource(t *testing.T) {
	reg := registry.New()
	// Registry.All() returns resources sorted by name; use
	// zero-padded names so alphabetical order matches the intended
	// tab order (a01 … a10) and "a10" genuinely sits at index 9.
	names := []string{"a01", "a02", "a03", "a04", "a05", "a06", "a07", "a08", "a09", "a10"}
	for _, n := range names {
		reg.Register(registry.Resource{Name: n, Plural: n + "s", Store: fakeStore{}})
	}
	a := newApp(context.Background(), reg, Options{Name: "test"})
	a.resource = ptr(reg.All()[0])
	var m tea.Model = a
	m, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m, _ = m.Update(tea.KeyPressMsg{Code: '0', Text: "0"})
	if got := m.(*app).resource.Name; got != "a10" {
		t.Errorf("after '0' with 10 resources, resource = %q, want a10", got)
	}
}

// TestNumberKeyOutOfRangeIsNoop confirms that pressing a digit beyond
// the registered count leaves the current resource untouched — the
// key falls through to dispatch which is harmless for bare digits.
func TestNumberKeyOutOfRangeIsNoop(t *testing.T) {
	reg := registry.New()
	reg.Register(registry.Resource{Name: "alpha", Plural: "alphas", Store: fakeStore{}})
	a := newApp(context.Background(), reg, Options{Name: "test"})
	a.resource = ptr(reg.All()[0])
	var m tea.Model = a
	m, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	// Only one resource registered; "2".."9" and "0" must be no-ops.
	for _, k := range []rune{'2', '3', '9', '0'} {
		m, _ = m.Update(tea.KeyPressMsg{Code: k, Text: string(k)})
		if got := m.(*app).resource.Name; got != "alpha" {
			t.Errorf("after %q with 1 resource, resource = %q, want alpha (unchanged)", string(k), got)
		}
	}
}

// TestResourceHintsMatchDigitKeys pins the top-of-screen tab labels to
// the same 1-based numeric mapping used by the digit dispatcher: the
// first resource is labeled "<1>", the ninth "<9>", and the tenth
// wraps to "<0>". Regression guard for the previous 0-based labels
// getting out of sync with the key handler.
func TestResourceHintsMatchDigitKeys(t *testing.T) {
	reg := registry.New()
	names := []string{"a01", "a02", "a03", "a04", "a05", "a06", "a07", "a08", "a09", "a10"}
	for _, n := range names {
		reg.Register(registry.Resource{Name: n, Plural: n + "s", Store: fakeStore{}})
	}
	a := newApp(context.Background(), reg, Options{Name: "test"})
	a.resource = ptr(reg.All()[0])
	var m tea.Model = a
	m, _ = m.Update(tea.WindowSizeMsg{Width: 200, Height: 30})
	plain := stripANSI(m.(*app).View().Content)

	// The hint row lives in the top header block and should pair each
	// digit with the corresponding plural, in registration order.
	expected := []string{
		"<1> a01s", "<2> a02s", "<3> a03s", "<4> a04s", "<5> a05s",
		"<6> a06s", "<7> a07s", "<8> a08s", "<9> a09s", "<0> a10s",
	}
	for _, want := range expected {
		if !strings.Contains(plain, want) {
			t.Errorf("resource hints missing %q:\n%s", want, plain)
		}
	}
	// And the old 0-based label for the first tab must be gone.
	if strings.Contains(plain, "<0> a01s") {
		t.Errorf("resource hints still advertise '<0> a01s' — the first tab should be '<1>'")
	}
}

func TestPaletteOpensOnColon(t *testing.T) {
	reg := registry.New()
	reg.Register(registry.Resource{Name: "thing", Plural: "things", Store: fakeStore{}})
	a := newApp(context.Background(), reg, Options{Name: "test"})
	a.resource = ptr(reg.All()[0])
	var m tea.Model = a
	m, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m, _ = m.Update(tea.KeyPressMsg{Code: ':', Text: ":"})
	if !m.(*app).showPalette {
		t.Error("showPalette not set after ':'")
	}
}

func TestHelpToggle(t *testing.T) {
	reg := registry.New()
	reg.Register(registry.Resource{Name: "thing", Plural: "things", Store: fakeStore{}})
	a := newApp(context.Background(), reg, Options{Name: "test"})
	a.resource = ptr(reg.All()[0])
	var m tea.Model = a
	m, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m, _ = m.Update(tea.KeyPressMsg{Code: '?', Text: "?"})
	if !m.(*app).showHelp {
		t.Fatal("help not shown after '?'")
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape, Text: "esc"})
	if m.(*app).showHelp {
		t.Error("help still shown after 'esc'")
	}
}

func TestTerminalTooSmallRendersEmpty(t *testing.T) {
	reg := registry.New()
	a := newApp(context.Background(), reg, Options{Name: "test"})
	var m tea.Model = a
	m, _ = m.Update(tea.WindowSizeMsg{Width: 20, Height: 5})
	v := m.(*app).View()
	if v.Content != "" {
		t.Errorf("expected empty view for tiny terminal, got %q", v.Content)
	}
}

// streamingStore emits two batches, then closes.
type streamingStore struct{}

func (streamingStore) Get(_ context.Context, _ string) (any, error) { return nil, nil }
func (streamingStore) List(_ context.Context, _ registry.Filter) ([]any, error) {
	return nil, nil
}
func (streamingStore) StreamList(_ context.Context, _ registry.Filter) <-chan registry.Batch {
	ch := make(chan registry.Batch, 3)
	ch <- registry.Batch{Items: []any{"a", "b"}}
	ch <- registry.Batch{Items: []any{"c"}}
	close(ch)
	return ch
}

func TestStreamStoreAppends(t *testing.T) {
	reg := registry.New()
	reg.Register(registry.Resource{Name: "s", Plural: "ss", Store: streamingStore{}})
	a := newApp(context.Background(), reg, Options{Name: "test"})
	a.resource = ptr(reg.All()[0])
	var m tea.Model = a
	m, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	// Kick off a refresh and pump itemsMsgs until we see final.
	cmd := a.refresh()
	for i := 0; i < 10 && cmd != nil; i++ {
		msg := cmd()
		im, ok := msg.(itemsMsg)
		if !ok {
			break
		}
		var c tea.Cmd
		m, c = m.Update(im)
		cmd = c
		if im.final {
			break
		}
	}
	if got := len(m.(*app).items); got != 3 {
		t.Errorf("streamed items = %d, want 3", got)
	}
}

func TestContextDisplayedInFooter(t *testing.T) {
	reg := registry.New()
	reg.Register(registry.Resource{Name: "thing", Plural: "things", Store: fakeStore{}})
	a := newApp(context.Background(), reg, Options{
		Name:    "test",
		Context: func() string { return "us-east-2" },
	})
	a.resource = ptr(reg.All()[0])
	var m tea.Model = a
	m, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	plain := stripANSI(m.(*app).View().Content)
	if !strings.Contains(plain, "us-east-2") {
		t.Errorf("context label not in view:\n%s", plain)
	}
}

// getCountingStore records how many times Get has been called so tests
// can assert that detail refetches are actually issued.
type getCountingStore struct {
	gets int
	item any
}

func (s *getCountingStore) Get(_ context.Context, _ string) (any, error) {
	s.gets++
	return s.item, nil
}

func (s *getCountingStore) List(_ context.Context, _ registry.Filter) ([]any, error) {
	if s.item != nil {
		return []any{s.item}, nil
	}
	return nil, nil
}

// drainCmd runs cmd (and any follow-on cmds it returns) synchronously
// and threads the resulting msgs through m.Update until the tree is
// drained. Blocking commands (e.g. subscribeBus waiting on a channel)
// are bounded by a short per-command timeout so they don't hang the
// test. depth caps the recursion to prevent runaway pumps.
func drainCmd(t *testing.T, m tea.Model, cmd tea.Cmd, depth int) tea.Model {
	t.Helper()
	if cmd == nil || depth <= 0 {
		return m
	}
	msg := runCmdWithTimeout(cmd)
	if msg == nil {
		return m
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			m = drainCmd(t, m, c, depth-1)
		}
		return m
	}
	var next tea.Cmd
	m, next = m.Update(msg)
	return drainCmd(t, m, next, depth-1)
}

// runCmdWithTimeout executes cmd in a goroutine, returning its msg or
// nil if it doesn't complete within 50ms. This is how we skip
// subscribeBus (which blocks on a <-chan Event that never fires in
// tests) without rewriting the production code to accept a mock bus.
func runCmdWithTimeout(cmd tea.Cmd) tea.Msg {
	if cmd == nil {
		return nil
	}
	done := make(chan tea.Msg, 1)
	go func() {
		defer func() { _ = recover() }() // tea.Tick under test can panic; ignore
		done <- cmd()
	}()
	select {
	case msg := <-done:
		return msg
	case <-time.After(50 * time.Millisecond):
		return nil
	}
}

func TestSagaDoneInDetailModeRefetchesDetail(t *testing.T) {
	// Regression: after a deploy (or any saga) completes while the
	// detail view is up, the detail pane must refetch so fields the
	// saga mutated (last-deploy, container status) reflect reality.
	store := &getCountingStore{item: detailWidget{Name: "hello"}}
	reg := registry.New()
	reg.Register(registry.Resource{
		Name:   "thing",
		Plural: "things",
		Store:  store,
		Fields: []registry.Field{{Name: "Name", Flag: "name", Table: registry.TableHint{Header: "NAME"}}},
	})
	a := newApp(context.Background(), reg, Options{Name: "test"})
	a.resource = ptr(reg.All()[0])
	a.items = []any{detailWidget{Name: "hello"}}
	a.mode = modeDetail
	a.cursor = 0
	var m tea.Model = a
	m, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})

	before := store.gets

	// Simulate a saga-done event from the runtime bus.
	doneEv := sagaEventMsg(runtime.Event{
		Saga:     "deploy",
		Resource: "thing",
		Done:     true,
		Status:   runtime.OK,
	})
	_, cmd := m.Update(doneEv)
	_ = drainCmd(t, m, cmd, 16)

	if got := store.gets - before; got < 1 {
		t.Errorf("expected at least one Get after saga done in detail mode; got %d", got)
	}
}

func TestSagaDoneInListModeSkipsDetailRefetch(t *testing.T) {
	// Symmetry guard: when the saga fires while the user is on the
	// LIST view, we should NOT issue a Get — Get is only for the
	// currently-rendered detail pane.
	store := &getCountingStore{item: detailWidget{Name: "hello"}}
	reg := registry.New()
	reg.Register(registry.Resource{
		Name:   "thing",
		Plural: "things",
		Store:  store,
		Fields: []registry.Field{{Name: "Name", Flag: "name", Table: registry.TableHint{Header: "NAME"}}},
	})
	a := newApp(context.Background(), reg, Options{Name: "test"})
	a.resource = ptr(reg.All()[0])
	a.items = []any{detailWidget{Name: "hello"}}
	a.mode = modeList
	a.cursor = 0
	var m tea.Model = a
	m, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})

	before := store.gets

	doneEv := sagaEventMsg(runtime.Event{
		Saga:     "deploy",
		Resource: "thing",
		Done:     true,
		Status:   runtime.OK,
	})
	_, cmd := m.Update(doneEv)
	_ = drainCmd(t, m, cmd, 16)

	if got := store.gets - before; got != 0 {
		t.Errorf("list mode must not issue Get after saga done; got %d extra Gets", got)
	}
}

func TestTickInDetailModeRefetchesDetail(t *testing.T) {
	// The periodic refresh tick should keep the detail pane warm too,
	// not just the list. Otherwise a user parked on a detail view
	// would see frozen state until they navigate away and back.
	store := &getCountingStore{item: detailWidget{Name: "hello"}}
	reg := registry.New()
	reg.Register(registry.Resource{
		Name:   "thing",
		Plural: "things",
		Store:  store,
		Fields: []registry.Field{{Name: "Name", Flag: "name", Table: registry.TableHint{Header: "NAME"}}},
	})
	a := newApp(context.Background(), reg, Options{Name: "test"})
	a.resource = ptr(reg.All()[0])
	a.items = []any{detailWidget{Name: "hello"}}
	a.mode = modeDetail
	a.cursor = 0
	var m tea.Model = a
	m, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})

	before := store.gets
	_, cmd := m.Update(tickMsg{})
	_ = drainCmd(t, m, cmd, 16)

	if got := store.gets - before; got < 1 {
		t.Errorf("expected at least one Get after tick in detail mode; got %d", got)
	}
}

// TestMergeByPrimaryKey exercises the merge-by-first-field logic that
// backs Batch.Replace in the TUI.
func TestMergeByPrimaryKey(t *testing.T) {
	type row struct {
		Name   string
		Status string
	}
	res := &registry.Resource{
		Fields: []registry.Field{
			{Name: "Name"},
			{Name: "Status"},
		},
	}

	t.Run("updates_existing_row", func(t *testing.T) {
		existing := []any{row{Name: "a", Status: "loading…"}, row{Name: "b", Status: "loading…"}}
		updates := []any{row{Name: "a", Status: "healthy"}}
		got := mergeByPrimaryKey(existing, updates, res)
		if len(got) != 2 {
			t.Fatalf("want 2, got %d", len(got))
		}
		if got[0].(row).Status != "healthy" {
			t.Errorf("row 'a' not updated: %+v", got[0])
		}
		if got[1].(row).Status != "loading…" {
			t.Errorf("row 'b' should be untouched: %+v", got[1])
		}
	})

	t.Run("appends_unmatched_row", func(t *testing.T) {
		existing := []any{row{Name: "a", Status: "loading…"}}
		updates := []any{row{Name: "b", Status: "healthy"}}
		got := mergeByPrimaryKey(existing, updates, res)
		if len(got) != 2 {
			t.Fatalf("want 2, got %d", len(got))
		}
		if got[1].(row).Name != "b" {
			t.Errorf("unmatched row not appended: %+v", got)
		}
	})

	t.Run("preserves_existing_order", func(t *testing.T) {
		existing := []any{
			row{Name: "c", Status: "loading…"},
			row{Name: "a", Status: "loading…"},
			row{Name: "b", Status: "loading…"},
		}
		updates := []any{row{Name: "a", Status: "healthy"}}
		got := mergeByPrimaryKey(existing, updates, res)
		if got[0].(row).Name != "c" || got[1].(row).Name != "a" || got[2].(row).Name != "b" {
			t.Errorf("order lost: %+v", got)
		}
	})

	t.Run("empty_updates_is_noop", func(t *testing.T) {
		existing := []any{row{Name: "a", Status: "loading…"}}
		got := mergeByPrimaryKey(existing, nil, res)
		if len(got) != 1 || got[0].(row).Name != "a" {
			t.Errorf("want unchanged single row, got %+v", got)
		}
	})

	t.Run("no_fields_falls_back_to_append", func(t *testing.T) {
		existing := []any{row{Name: "a"}}
		updates := []any{row{Name: "a"}}
		got := mergeByPrimaryKey(existing, updates, &registry.Resource{})
		if len(got) != 2 {
			t.Fatalf("expected append fallback, got %d", len(got))
		}
	})
}

// TestRenderAsyncCell exercises the four states a Field.Async cell can
// render in. Focused on the branch logic — the actual spinner frame
// output varies by bubbletea version, so we just assert "not empty".
func TestRenderAsyncCell(t *testing.T) {
	reg := registry.New()
	reg.Register(registry.Resource{
		Name:   "thing",
		Plural: "things",
		Fields: []registry.Field{{Name: "Name"}},
		Store:  fakeStore{},
	})
	a := newApp(context.Background(), reg, Options{Name: "test"})
	a.resource = ptr(reg.All()[0])
	f := registry.Field{Name: "Status", Async: func(_ context.Context, _ any) (string, error) { return "", nil }}

	// Missing entry → spinner.
	if got := a.renderAsyncCell("row-a", f); got == "" {
		t.Errorf("missing entry: want spinner, got empty")
	}

	// Loaded with value → value, regardless of loading flag.
	a.async[asyncKey{resource: "thing", pk: "row-b", field: "Status"}] = &asyncState{
		value: "dev: 1/1", loaded: true,
	}
	if got := a.renderAsyncCell("row-b", f); got != "dev: 1/1" {
		t.Errorf("loaded value: got %q want 'dev: 1/1'", got)
	}

	// Stale reload: value still present, loading=true — must keep value.
	a.async[asyncKey{resource: "thing", pk: "row-b", field: "Status"}].loading = true
	if got := a.renderAsyncCell("row-b", f); got != "dev: 1/1" {
		t.Errorf("reload with prior value: got %q want persistent 'dev: 1/1'", got)
	}

	// Error with no prior value → muted dash.
	a.async[asyncKey{resource: "thing", pk: "row-c", field: "Status"}] = &asyncState{
		loaded: true, err: errTest,
	}
	got := a.renderAsyncCell("row-c", f)
	if !strings.Contains(stripANSI(got), "—") {
		t.Errorf("error branch: got %q want to contain '—'", got)
	}

	// Still-loading (not yet succeeded) → spinner (non-empty, no value).
	a.async[asyncKey{resource: "thing", pk: "row-d", field: "Status"}] = &asyncState{loading: true}
	if got := a.renderAsyncCell("row-d", f); got == "" {
		t.Errorf("in-flight: want spinner, got empty")
	}
}

var errTest = errForTest("boom")

type errForTest string

func (e errForTest) Error() string { return string(e) }

// TestOperationSortKeyClustersPairedOps pins the SortKey contract:
// ops with a non-empty SortKey sort by that value instead of by
// Name. The canonical use case is pairing add-target / remove-
// target so they land adjacent in the status bar and "?" help,
// instead of alphabetically split (a… vs r…).
func TestOperationSortKeyClustersPairedOps(t *testing.T) {
	type row struct{ Name string }
	reg := registry.New()
	reg.Register(registry.Resource{
		Name: "app", Plural: "apps", Store: fakeStore{},
		Fields: []registry.Field{
			{Name: "Name", Flag: "name", Table: registry.TableHint{Header: "NAME"}},
		},
		Operations: map[string]registry.Operation{
			"add-target": {
				Name: "add-target", Key: "t",
				Fields: []registry.Field{{Flag: "name", Required: true}},
				Steps:  []registry.Step{{Label: "n", Do: func(context.Context, *registry.State) error { return nil }}},
			},
			// Would alphabetically sort BEFORE remove-target and
			// split the pair — SortKey overrides that.
			"delete": {
				Name: "delete", Key: "ctrl+d",
				Fields: []registry.Field{{Flag: "name", Required: true}},
				Steps:  []registry.Step{{Label: "n", Do: func(context.Context, *registry.State) error { return nil }}},
			},
			"remove-target": {
				Name: "remove-target", Key: "T",
				SortKey: "add-target-remove",
				Fields:  []registry.Field{{Flag: "name", Required: true}},
				Steps:   []registry.Step{{Label: "n", Do: func(context.Context, *registry.State) error { return nil }}},
			},
		},
	})
	a := newApp(context.Background(), reg, Options{Name: "test"})
	a.resource = ptr(reg.All()[0])
	a.items = []any{row{Name: "my-app"}}
	a.cursor = 0

	// Collect ops from the key map in order; we only care about the
	// "Resource" category and that add-target / remove-target are
	// adjacent with add-target first.
	var resourceLabels []string
	for _, b := range a.keyMap() {
		if b.Cat != "Resource" {
			continue
		}
		resourceLabels = append(resourceLabels, b.Label)
	}

	addIdx, removeIdx := -1, -1
	for i, l := range resourceLabels {
		if l == "add-target" {
			addIdx = i
		}
		if l == "remove-target" {
			removeIdx = i
		}
	}
	if addIdx < 0 || removeIdx < 0 {
		t.Fatalf("add-target/remove-target missing from keymap: %v", resourceLabels)
	}
	if removeIdx != addIdx+1 {
		t.Errorf("remove-target at %d, add-target at %d; want remove-target immediately after add-target. Full order: %v",
			removeIdx, addIdx, resourceLabels)
	}
}

// TestOperationSortKeyEmptyFallsBackToName confirms backward
// compatibility: ops without a SortKey keep sorting by their Name,
// so existing resources don't shift order unexpectedly.
func TestOperationSortKeyEmptyFallsBackToName(t *testing.T) {
	type row struct{ Name string }
	reg := registry.New()
	reg.Register(registry.Resource{
		Name: "app", Plural: "apps", Store: fakeStore{},
		Fields: []registry.Field{
			{Name: "Name", Flag: "name", Table: registry.TableHint{Header: "NAME"}},
		},
		Operations: map[string]registry.Operation{
			"alpha": {
				Name: "alpha", Key: "a",
				Fields: []registry.Field{{Flag: "name", Required: true}},
				Steps:  []registry.Step{{Label: "n", Do: func(context.Context, *registry.State) error { return nil }}},
			},
			"beta": {
				Name: "beta", Key: "b",
				Fields: []registry.Field{{Flag: "name", Required: true}},
				Steps:  []registry.Step{{Label: "n", Do: func(context.Context, *registry.State) error { return nil }}},
			},
		},
	})
	a := newApp(context.Background(), reg, Options{Name: "test"})
	a.resource = ptr(reg.All()[0])
	a.items = []any{row{Name: "x"}}
	a.cursor = 0

	var labels []string
	for _, b := range a.keyMap() {
		if b.Cat != "Resource" {
			continue
		}
		labels = append(labels, b.Label)
	}
	if len(labels) < 2 || labels[0] != "alpha" || labels[1] != "beta" {
		t.Errorf("default alphabetical order lost: %v", labels)
	}
}
