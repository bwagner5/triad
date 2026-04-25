package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/bwagner5/triad/pkg/registry"
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
	a.resource = ptr(reg.All()[0]) // alpha
	var m tea.Model = a
	m, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m, _ = m.Update(tea.KeyPressMsg{Code: '1', Text: "1"})
	if got := m.(*app).resource.Name; got != "beta" {
		t.Errorf("after '1', resource = %q, want beta", got)
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
