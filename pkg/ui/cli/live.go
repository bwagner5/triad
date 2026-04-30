package cli

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/spinner"
	"github.com/bwagner5/triad/pkg/runtime"
	"github.com/bwagner5/triad/pkg/ui/theme"
)

// RenderEventsLive renders saga events inline with a header, per-step
// spinners/checkmarks, and a progress bar that ticks as steps complete.
// NeedsInput events suspend rendering, prompt for the missing fields via
// wizard.Collect, and then resume.
func RenderEventsLive(ch <-chan runtime.Event) error {
	m := &liveModel{
		ctx: context.Background(),
		ch:  ch,
		sp:  spinner.New(),
		bar: progress.New(progress.WithDefaultBlend(), progress.WithoutPercentage()),
	}
	fm, err := tea.NewProgram(m).Run()
	if err != nil {
		return err
	}
	return fm.(*liveModel).err
}

type liveModel struct {
	ctx    context.Context
	ch     <-chan runtime.Event
	sp     spinner.Model
	bar    progress.Model
	steps  []runtime.Event // latest state per step index
	total  int             // len(op.Steps) including skipped
	saga   string
	done   bool
	err    error
	output string
	width  int
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
		if e.Total > 0 {
			m.total = e.Total
		}
		if e.Status == runtime.NeedsInput {
			// Suspend the live renderer, run the inline wizard on the real
			// tty, and hand results back via Provide. Then resume.
			return m, m.needInput(e)
		}
		if e.Done {
			m.done = true
			m.err = e.Err
			m.output = e.Output
			return m, waitEvent(m.ch)
		}
		for len(m.steps) <= e.Index {
			m.steps = append(m.steps, runtime.Event{})
		}
		m.steps[e.Index] = e
		return m, waitEvent(m.ch)
	case waitEventMsg:
		return m, waitEvent(m.ch)
	case closedMsg:
		return m, tea.Quit
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.sp, cmd = m.sp.Update(msg)
		return m, cmd
	case tea.WindowSizeMsg:
		m.width = msg.Width
		// Cap the progress bar so it fits nicely inside the terminal.
		w := msg.Width - 20
		if w < 20 {
			w = 20
		}
		if w > 60 {
			w = 60
		}
		m.bar.SetWidth(w)
		return m, nil
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
	}
	return m, nil
}

// needInput runs wizard.Collect over the step's requested fields via
// tea.Exec (suspends the renderer), then calls Provide and resumes the
// saga. Esc / wizard error is delivered as nil answer, which the runtime
// treats as "abort".
func (m *liveModel) needInput(e runtime.Event) tea.Cmd {
	return tea.Exec(&wizardExec{
		ctx:     m.ctx,
		need:    e.Needs,
		provide: e.Provide,
	}, func(err error) tea.Msg {
		_ = err
		return waitEventMsg{}
	})
}

type waitEventMsg struct{}

// progressCounts returns (completed, total) excluding skipped steps so
// the counter and bar reflect real work, not boilerplate. completed
// counts OK + Failed; running isn't counted yet.
func (m *liveModel) progressCounts() (completed, total int) {
	if m.total > 0 {
		total = m.total
	} else {
		total = len(m.steps)
	}
	// Subtract skipped from total and count completed work.
	skipped := 0
	for _, e := range m.steps {
		switch e.Status {
		case runtime.Skipped:
			skipped++
		case runtime.OK, runtime.Failed:
			completed++
		}
	}
	total -= skipped
	if total < 0 {
		total = 0
	}
	return completed, total
}

func (m *liveModel) View() tea.View {
	var b strings.Builder

	// Header: saga title + "N / M" counter. Only shown once we've
	// received at least one event so the very first frame doesn't
	// flash an empty header.
	if m.saga != "" {
		completed, total := m.progressCounts()
		title := "Deploying"
		if m.saga != "deploy" {
			title = cap1(m.saga)
		}
		header := theme.Label.Render(title)
		if total > 0 {
			header += "  " + theme.MutedText.Render(
				fmt.Sprintf("%d / %d", completed, total))
		}
		b.WriteString(header + "\n\n")
	}

	// Per-step lines. Skipped steps are suppressed entirely.
	for _, e := range m.steps {
		if e.Status == runtime.Skipped {
			continue
		}
		mark := theme.PendMark
		switch e.Status {
		case runtime.Running:
			mark = m.sp.View()
		case runtime.OK:
			mark = theme.OKMark
		case runtime.Failed:
			mark = theme.ErrMark
		}
		label := e.Step
		if e.Status == runtime.Failed && e.Err != nil {
			label += " — " + theme.Err.Render(e.Err.Error())
		}
		b.WriteString(mark + " " + label + "\n")
	}

	// Progress bar: only while the saga is in flight. Hidden on the
	// final frame so the summary isn't visually crowded.
	if m.saga != "" && !m.done {
		completed, total := m.progressCounts()
		if total > 0 {
			pct := float64(completed) / float64(total)
			b.WriteString("\n" + m.bar.ViewAs(pct) + "\n")
		}
	}

	if m.done {
		if m.err != nil {
			b.WriteString("\n" + theme.Err.Render("✗ "+m.saga+" failed: "+m.err.Error()) + "\n")
		} else {
			b.WriteString("\n" + theme.OK.Render("✓ "+m.saga+" complete") + "\n")
			if m.output != "" {
				b.WriteString("\n" + m.output + "\n")
			}
		}
	}
	return tea.NewView(b.String())
}

// cap1 turns saga names like "deploy" → "Deploying" feel: we accept the
// saga string and produce a human header. Hyphens become spaces, each
// word is title-cased. "deploy" is special-cased to the verbing form
// used in the header ("Deploying"). Other sagas get their bare name
// title-cased (e.g. "enable-gh-action" → "Enable Gh Action").
func cap1(s string) string {
	if s == "" {
		return s
	}
	parts := strings.Split(s, "-")
	for i, p := range parts {
		if p == "" {
			continue
		}
		r := []rune(p)
		if r[0] >= 'a' && r[0] <= 'z' {
			r[0] -= 32
		}
		parts[i] = string(r)
	}
	return strings.Join(parts, " ")
}
