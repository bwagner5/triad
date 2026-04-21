package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/bwagner5/go-cli-template/pkg/registry"
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
