package tui

import (
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/bwagner5/go-cli-template/pkg/runtime"
	"github.com/bwagner5/go-cli-template/pkg/ui/theme"
)

// sagaOverlay shows live step progress for a running saga.
type sagaOverlay struct {
	active bool
	name   string
	events []runtime.Event // latest event per step index
	done   bool
	err    error
	w, h   int
}

func newSagaOverlay() sagaOverlay { return sagaOverlay{} }

func (s *sagaOverlay) SetSize(w, h int) { s.w, s.h = w, h }
func (s *sagaOverlay) Active() bool     { return s.active }
func (s *sagaOverlay) Clear()           { *s = sagaOverlay{w: s.w, h: s.h} }

func (s *sagaOverlay) Start(name string) {
	s.active = true
	s.name = name
	s.events = nil
	s.done = false
	s.err = nil
}

func (s *sagaOverlay) Push(e runtime.Event) {
	if !s.active {
		s.active = true
		s.name = e.Saga
	}
	if e.Done {
		s.done = true
		s.err = e.Err
		return
	}
	// Update the slot at e.Index (grow slice as needed).
	for len(s.events) <= e.Index {
		s.events = append(s.events, runtime.Event{})
	}
	s.events[e.Index] = e
}

// DismissAfter returns a command that sends dismissSagaMsg after a few seconds.
func (s *sagaOverlay) DismissAfter() tea.Cmd {
	d := 3 * time.Second
	if s.err != nil {
		d = 6 * time.Second
	}
	return tea.Tick(d, func(_ time.Time) tea.Msg { return dismissSagaMsg{} })
}

func (s *sagaOverlay) Box(w, _ int) string {
	header := theme.Heading.Render("Running: " + s.name)
	lines := header + "\n\n"
	for _, e := range s.events {
		mark := theme.PendMark
		switch e.Status {
		case runtime.Running:
			mark = theme.RunMark
		case runtime.OK:
			mark = theme.OKMark
		case runtime.Failed:
			mark = theme.ErrMark
		case runtime.Skipped:
			mark = theme.SkipMark
		}
		label := e.Step
		if label == "" {
			label = theme.MutedText.Render("(pending)")
		}
		lines += fmt.Sprintf("  %s %s\n", mark, label)
	}
	if s.done {
		lines += "\n"
		if s.err != nil {
			lines += theme.Err.Render("✗ failed: "+s.err.Error()) + "\n"
		} else {
			lines += theme.OK.Render("✓ complete") + "\n"
		}
		lines += "\n" + theme.MutedText.Render("press esc or enter to close")
	}
	width := 60
	if w < width+4 {
		width = w - 4
	}
	return theme.Border.Width(width).Render(lines)
}
