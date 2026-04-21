package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/bwagner5/go-cli-template/pkg/registry"
)

// viewSnapshot drives a minimal WindowSizeMsg and renders the view to a string.
func viewSnapshot(t *testing.T, w, h int) string {
	t.Helper()
	reg := registry.New()
	a := newApp(context.Background(), reg)
	var m tea.Model = a
	m, _ = m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	v := m.(*app).View()
	return v.Content
}

// TestStatusBarOnLastRow asserts the rendered view has the status-bar keys
// (":", "?", "q") on the last line.
func TestStatusBarOnLastRow(t *testing.T) {
	out := viewSnapshot(t, 80, 24)
	// Strip ANSI for inspection.
	plain := stripANSI(out)
	lines := strings.Split(plain, "\n")
	// Drop the trailing empty element from the final newline if present.
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) < 24 {
		t.Fatalf("expected 24 lines, got %d\nfull:\n%q", len(lines), plain)
	}
	last := lines[len(lines)-1]
	for _, want := range []string{":", "?", "q", "palette", "help", "quit"} {
		if !strings.Contains(last, want) {
			t.Errorf("last line missing %q\nlast=%q\nfull:\n%s", want, last, plain)
		}
	}
}

// TestHelpOverlayCentered asserts the help modal is roughly centered in both
// axes when toggled.
func TestHelpOverlayCentered(t *testing.T) {
	reg := registry.New()
	a := newApp(context.Background(), reg)
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
func stripANSI(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		if r == 0x1b {
			in = true
			continue
		}
		if in {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				in = false
			}
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
