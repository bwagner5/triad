package cli

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/spinner"
	"github.com/bwagner5/triad/pkg/runtime"
	"github.com/bwagner5/triad/pkg/ui/theme"
)

// RenderEventsLive renders saga events inline with a single spinner that
// rewrites itself: the running step shows a spinner, completed steps show
// a checkmark, failures show a cross. One line per step — no intermediate
// "started" lines.
func RenderEventsLive(ch <-chan runtime.Event) error {
	m := &liveModel{ch: ch, sp: spinner.New()}
	fm, err := tea.NewProgram(m).Run()
	if err != nil {
		return err
	}
	return fm.(*liveModel).err
}

type liveModel struct {
	ch     <-chan runtime.Event
	sp     spinner.Model
	steps  []runtime.Event // latest state per step index
	saga   string
	done   bool
	err    error
}

type eventMsg runtime.Event
type closedMsg struct{}

func waitEvent(ch <-chan runtime.Event) tea.Cmd {
	return func() tea.Msg {
		e, ok := <-ch
		if !ok {
			return closedMsg{}
		}
		return eventMsg(e)
	}
}

func (m *liveModel) Init() tea.Cmd { return tea.Batch(m.sp.Tick, waitEvent(m.ch)) }

func (m *liveModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case eventMsg:
		e := runtime.Event(msg)
		m.saga = e.Saga
		if e.Done {
			m.done = true
			m.err = e.Err
			return m, waitEvent(m.ch)
		}
		for len(m.steps) <= e.Index {
			m.steps = append(m.steps, runtime.Event{})
		}
		m.steps[e.Index] = e
		return m, waitEvent(m.ch)
	case closedMsg:
		return m, tea.Quit
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.sp, cmd = m.sp.Update(msg)
		return m, cmd
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *liveModel) View() tea.View {
	s := ""
	for _, e := range m.steps {
		mark := theme.PendMark
		switch e.Status {
		case runtime.Running:
			mark = m.sp.View()
		case runtime.OK:
			mark = theme.OKMark
		case runtime.Failed:
			mark = theme.ErrMark
		case runtime.Skipped:
			mark = theme.SkipMark
		}
		label := e.Step
		if e.Status == runtime.Failed && e.Err != nil {
			label += " — " + theme.Err.Render(e.Err.Error())
		}
		s += mark + " " + label + "\n"
	}
	if m.done {
		if m.err != nil {
			s += theme.Err.Render("✗ "+m.saga+" failed: "+m.err.Error()) + "\n"
		} else {
			s += theme.OK.Render("✓ "+m.saga+" complete") + "\n"
		}
	}
	return tea.NewView(s)
}
