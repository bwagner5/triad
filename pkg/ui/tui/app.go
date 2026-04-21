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
	"reflect"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"
	"github.com/bwagner5/go-cli-template/pkg/registry"
	"github.com/bwagner5/go-cli-template/pkg/runtime"
	"github.com/bwagner5/go-cli-template/pkg/ui/theme"
)

func reflectIndirect(v any) reflect.Value { return reflect.Indirect(reflect.ValueOf(v)) }
func readField(rv reflect.Value, f registry.Field) string { return read(rv, f) }

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
	w, h := a.width, a.height
	if w < 20 || h < 6 {
		return tea.NewView("")
	}

	topBar := a.renderTopBar()       // 1 row, full width
	statusBar := a.renderStatusBar() // 1 row, full width
	bodyH := h - 2                   // top + bottom bars
	if bodyH < 1 {
		bodyH = 1
	}
	body := lipgloss.NewStyle().
		Width(w).
		Height(bodyH).
		MaxHeight(bodyH).
		Padding(1, 2).
		Render(a.renderBody(bodyH - 2)) // -2 for padding rows

	base := lipgloss.JoinVertical(lipgloss.Left, topBar, body, statusBar)

	// Collect overlays (if any) and nest them inside the base layer so
	// absolute X/Y positioning works.
	var overlays []*lipgloss.Layer
	if a.showHelp {
		// Dim layer covers the whole background.
		dim := lipgloss.NewStyle().
			Width(w).Height(h).
			Foreground(theme.Muted).
			Render("")
		overlays = append(overlays, lipgloss.NewLayer(dim).X(0).Y(0).Z(1))
		overlays = append(overlays, centeredLayer(a.help.Box(w, h), w, h, 2))
	}
	if a.showPalette {
		overlays = append(overlays, centeredLayer(a.palette.Box(w, h), w, h, 3))
	}
	if a.saga.Active() {
		overlays = append(overlays, centeredLayer(a.saga.Box(w, h), w, h, 4))
	}

	root := lipgloss.NewLayer(base)
	if len(overlays) > 0 {
		root = root.AddLayers(overlays...)
	}
	v := tea.NewView(lipgloss.NewCompositor(root).Render())
	v.AltScreen = true
	return v
}

// centeredLayer builds a layer whose top-left is placed so the content is
// centered within a w×h area.
func centeredLayer(content string, w, h, z int) *lipgloss.Layer {
	cw := lipgloss.Width(content)
	ch := lipgloss.Height(content)
	x := (w - cw) / 2
	y := (h - ch) / 2
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	return lipgloss.NewLayer(content).X(x).Y(y).Z(z)
}

func (a *app) renderTopBar() string {
	brand := theme.Heading.Render(" go-cli-template ")
	resName := ""
	if a.resource != nil {
		resName = strings.ToUpper(a.resource.Plural)
	}
	crumb := theme.Label.Render(resName)
	right := theme.MutedText.Render(fmt.Sprintf("%d items ", len(a.items)))
	// Left block and right block; middle filler.
	left := brand + "  " + crumb
	filler := a.width - lipgloss.Width(left) - lipgloss.Width(right)
	if filler < 1 {
		filler = 1
	}
	line := left + strings.Repeat(" ", filler) + right
	return lipgloss.NewStyle().
		Width(a.width).
		Background(theme.Subtle).
		Foreground(theme.Text).
		Render(line)
}

func (a *app) renderBody(maxH int) string {
	if a.resource == nil {
		return theme.MutedText.Render("no resources registered")
	}
	if a.err != nil {
		return theme.Err.Render(a.err.Error())
	}
	detail := ""
	detailH := 0
	if a.mode == modeDetail && len(a.items) > 0 {
		detail = theme.Label.Render("DETAIL") + "\n" + detailFor(a.resource, a.items[a.cursor])
		detailH = lipgloss.Height(detail) + 1
	}
	rowBudget := maxH - detailH
	if rowBudget < 1 {
		rowBudget = 1
	}
	t := a.renderTable(rowBudget)
	if detail == "" {
		return t
	}
	return t + "\n" + detail
}

// renderTable builds a lipgloss table with headers and a highlighted cursor row.
func (a *app) renderTable(maxRows int) string {
	var headers []string
	var fields []registry.Field
	for _, f := range a.resource.Fields {
		if f.Table.Header == "" || f.Table.Wide {
			continue
		}
		headers = append(headers, f.Table.Header)
		fields = append(fields, f)
	}
	// Leave one header row in the budget.
	visible := maxRows - 1
	if visible < 1 {
		visible = 1
	}
	start := 0
	if a.cursor >= visible {
		start = a.cursor - visible + 1
	}
	end := start + visible
	if end > len(a.items) {
		end = len(a.items)
	}
	var rows [][]string
	cursorRow := -1
	for i := start; i < end; i++ {
		rv := reflectIndirect(a.items[i])
		row := make([]string, len(fields))
		for j, f := range fields {
			row[j] = readField(rv, f)
		}
		if i == a.cursor {
			cursorRow = i - start
		}
		rows = append(rows, row)
	}
	t := table.New().
		Headers(headers...).
		Rows(rows...).
		Border(lipgloss.HiddenBorder()).
		StyleFunc(func(row, _ int) lipgloss.Style {
			switch {
			case row == table.HeaderRow:
				return theme.Label.PaddingRight(2)
			case row == cursorRow:
				return lipgloss.NewStyle().
					Foreground(theme.Text).
					Background(theme.Subtle).
					Bold(true).
					PaddingRight(2)
			default:
				return lipgloss.NewStyle().PaddingRight(2)
			}
		})
	return t.Render()
}

func (a *app) renderStatusBar() string {
	keys := []string{
		theme.Key.Render(":") + " palette",
		theme.Key.Render("?") + " help",
		theme.Key.Render("r") + " refresh",
		theme.Key.Render("enter") + " detail",
		theme.Key.Render("q") + " quit",
	}
	bar := " "
	for i, k := range keys {
		if i > 0 {
			bar += theme.MutedText.Render("  ·  ")
		}
		bar += k
	}
	// Pad to full width so the background fills the row.
	return lipgloss.NewStyle().
		Width(a.width).
		Background(theme.Subtle).
		Render(bar)
}

// dismissSagaMsg clears the saga overlay after the auto-dismiss timer fires.
type dismissSagaMsg struct{}
