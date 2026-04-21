// Package tui is a k9s-inspired full-screen UI that drives the registry.
//
// Screens:
//   - list: shows rows of a resource, polled on the refresh scheduler
//   - detail: one-item view
//
// Overlays:
//   - palette (":"): fuzzy-filtered list of every resource and action
//   - help ("?"): dimmed screen + modal listing key bindings
//   - saga: live step progress when a saga is running
package tui

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/bwagner5/go-cli-template/pkg/registry"
	"github.com/bwagner5/go-cli-template/pkg/runtime"
	"github.com/bwagner5/go-cli-template/pkg/ui/theme"
)

// Run starts the full-screen TUI against the given registry.
func Run(ctx context.Context, reg *registry.Registry) error {
	m := newApp(ctx, reg)
	_, err := tea.NewProgram(m, tea.WithContext(ctx)).Run()
	return err
}

// ---- Model ----

type mode int

const (
	modeList mode = iota
	modeDetail
)

type app struct {
	ctx   context.Context
	reg   *registry.Registry
	sched *runtime.Scheduler

	width, height int
	mode          mode
	resource      *registry.Resource
	items         []any
	cursor        int
	err           error

	palette paletteModel
	help    helpOverlay
	saga    sagaOverlay

	showPalette bool
	showHelp    bool
}

func newApp(ctx context.Context, reg *registry.Registry) *app {
	a := &app{ctx: ctx, reg: reg, sched: runtime.NewScheduler(), palette: newPalette(reg), help: newHelp(), saga: newSagaOverlay()}
	if all := reg.All(); len(all) > 0 {
		r := all[0]
		a.resource = &r
	}
	return a
}

// ---- Messages ----

type tickMsg struct{}
type itemsMsg struct {
	resource string
	items    []any
	err      error
}
type sagaEventMsg runtime.Event

func (a *app) Init() tea.Cmd {
	return tea.Batch(a.refresh(), a.subscribeBus())
}

func (a *app) refresh() tea.Cmd {
	if a.resource == nil {
		return nil
	}
	res := a.resource
	return func() tea.Msg {
		items, err := res.Store.List(a.ctx, registry.Filter{})
		return itemsMsg{resource: res.Name, items: items, err: err}
	}
}

func (a *app) scheduleRefresh() tea.Cmd {
	if a.resource == nil {
		return nil
	}
	d := a.sched.Interval(a.resource.Name)
	return tea.Tick(d, func(_ time.Time) tea.Msg { return tickMsg{} })
}

// ---- Update ----

func (a *app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		a.palette.SetSize(msg.Width, msg.Height)
		a.saga.SetSize(msg.Width, msg.Height)
	case tea.KeyPressMsg:
		return a.handleKey(msg)
	case itemsMsg:
		if a.resource != nil && msg.resource == a.resource.Name {
			a.items, a.err = msg.items, msg.err
			if a.cursor >= len(a.items) {
				a.cursor = 0
			}
		}
		return a, a.scheduleRefresh()
	case tickMsg:
		return a, tea.Batch(a.refresh())
	case sagaEventMsg:
		ev := runtime.Event(msg)
		a.saga.Push(ev)
		if ev.Done {
			a.sched.Bump(ev.Resource)
			return a, tea.Batch(a.saga.DismissAfter(), a.refresh(), a.subscribeBus())
		}
		return a, a.subscribeBus()
	case paletteResultMsg:
		a.showPalette = false
		return a.handlePaletteChoice(msg)
	case dismissSagaMsg:
		a.saga.Clear()
	}
	return a, nil
}

func (a *app) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	if a.showPalette {
		p, cmd := a.palette.Update(msg)
		a.palette = p
		return a, cmd
	}
	if a.showHelp {
		if key == "?" || key == "esc" || key == "q" {
			a.showHelp = false
		}
		return a, nil
	}

	switch key {
	case "q", "ctrl+c":
		return a, tea.Quit
	case ":":
		a.showPalette = true
		a.palette.Focus()
		return a, nil
	case "?":
		a.showHelp = true
		return a, nil
	case "j", "down":
		if a.cursor < len(a.items)-1 {
			a.cursor++
		}
	case "k", "up":
		if a.cursor > 0 {
			a.cursor--
		}
	case "enter":
		if a.mode == modeList && len(a.items) > 0 {
			a.mode = modeDetail
		}
	case "esc":
		a.mode = modeList
	case "r":
		return a, a.refresh()
	}
	return a, nil
}

func (a *app) handlePaletteChoice(msg paletteResultMsg) (tea.Model, tea.Cmd) {
	if msg.canceled || msg.entry == nil {
		return a, nil
	}
	e := msg.entry
	switch e.kind {
	case paletteNav:
		a.resource = e.resource
		a.mode = modeList
		a.cursor = 0
		return a, a.refresh()
	case paletteAction, paletteSaga:
		// Fire-and-forget: run with whatever defaults exist. The TUI does
		// not currently run an in-TUI wizard; users can use the CLI for
		// that. Sagas still stream into the overlay.
		if e.saga != nil && e.resource != nil {
			ch := runtime.Run(a.ctx, *e.resource, *e.saga, registry.Input{})
			a.saga.Start(e.saga.Name)
			return a, readSagaCmd(ch)
		}
	}
	return a, nil
}

// subscribeBus relays saga events (from CLI-originated sagas, if any) into the TUI.
func (a *app) subscribeBus() tea.Cmd {
	ch := runtime.DefaultBus().Subscribe()
	return func() tea.Msg {
		e, ok := <-ch
		if !ok {
			return nil
		}
		return sagaEventMsg(e)
	}
}

func readSagaCmd(ch <-chan runtime.Event) tea.Cmd {
	return func() tea.Msg {
		e, ok := <-ch
		if !ok {
			return dismissSagaMsg{}
		}
		return sagaEventMsg(e)
	}
}

// ---- View ----

func (a *app) View() tea.View {
	body := a.renderBody()
	status := a.renderStatusBar()
	content := lipgloss.JoinVertical(lipgloss.Left, body, status)

	// Compose overlays via lipgloss Canvas + Layers.
	canvas := lipgloss.NewCanvas(a.width, a.height)
	canvas.Compose(lipgloss.NewLayer(content))
	if a.showHelp {
		dim := lipgloss.NewStyle().Foreground(theme.Muted).Render(content)
		canvas = lipgloss.NewCanvas(a.width, a.height)
		canvas.Compose(lipgloss.NewLayer(dim))
		canvas.Compose(a.help.Layer(a.width, a.height))
	}
	if a.showPalette {
		canvas.Compose(a.palette.Layer(a.width, a.height))
	}
	if a.saga.Active() {
		canvas.Compose(a.saga.Layer(a.width, a.height))
	}
	v := tea.NewView(canvas.Render())
	v.AltScreen = true
	return v
}

func (a *app) renderBody() string {
	if a.resource == nil {
		return theme.MutedText.Render("no resources registered")
	}
	header := theme.Heading.Render(a.resource.Plural)
	if a.err != nil {
		return header + "\n" + theme.Err.Render(a.err.Error())
	}
	rowHeight := a.height - 4
	if rowHeight < 1 {
		rowHeight = 1
	}
	var rows []string
	for i, it := range a.items {
		marker := "  "
		if i == a.cursor {
			marker = theme.Key.Render("▸ ")
		}
		rows = append(rows, marker+summarize(a.resource, it))
		if len(rows) >= rowHeight {
			break
		}
	}
	body := header + "\n" + theme.MutedText.Render(fmt.Sprintf("%d items", len(a.items))) + "\n"
	for _, r := range rows {
		body += r + "\n"
	}
	if a.mode == modeDetail && len(a.items) > 0 {
		body += "\n" + theme.Label.Render("DETAIL") + "\n" + detailFor(a.resource, a.items[a.cursor])
	}
	return body
}

func (a *app) renderStatusBar() string {
	keys := []string{
		theme.Key.Render(":") + " palette",
		theme.Key.Render("?") + " help",
		theme.Key.Render("r") + " refresh",
		theme.Key.Render("enter") + " detail",
		theme.Key.Render("q") + " quit",
	}
	bar := ""
	for i, k := range keys {
		if i > 0 {
			bar += theme.MutedText.Render("  ·  ")
		}
		bar += k
	}
	return lipgloss.NewStyle().Width(a.width).Render(bar)
}

// dismissSagaMsg clears the saga overlay after the auto-dismiss timer fires.
type dismissSagaMsg struct{}
