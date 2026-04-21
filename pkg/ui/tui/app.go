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
	"github.com/bwagner5/go-cli-template/pkg/duration"
	"github.com/bwagner5/go-cli-template/pkg/registry"
	"github.com/bwagner5/go-cli-template/pkg/runtime"
	"github.com/bwagner5/go-cli-template/pkg/ui/ascii"
	"github.com/bwagner5/go-cli-template/pkg/ui/theme"
)

func reflectIndirect(v any) reflect.Value { return reflect.Indirect(reflect.ValueOf(v)) }
func readField(rv reflect.Value, f registry.Field) string { return read(rv, f) }

// Options configures the TUI.
type Options struct {
	// Name is the CLI name shown as the banner at the top of the screen.
	Name string
	// Logo, if non-empty, overrides the auto-generated ASCII banner derived
	// from Name. Supply a pre-styled, multi-line string.
	Logo string
}

// Run starts the full-screen TUI against the given registry.
func Run(ctx context.Context, reg *registry.Registry, opts Options) error {
	m := newApp(ctx, reg, opts)
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
	opts  Options

	width, height int
	mode          mode
	resource      *registry.Resource
	items         []any
	cursor        int
	err           error

	// Refresh bookkeeping for the bottom-border counter.
	lastRefresh time.Time
	nextRefresh time.Time

	// Toast flash messages (top of screen).
	toast *toast

	palette paletteModel
	help    helpOverlay
	saga    sagaOverlay

	showPalette bool
	showHelp    bool
}

func newApp(ctx context.Context, reg *registry.Registry, opts Options) *app {
	if opts.Name == "" {
		opts.Name = "cli"
	}
	if opts.Logo == "" {
		opts.Logo = lipgloss.NewStyle().Foreground(theme.Warning).Render(ascii.Render(opts.Name))
	}
	a := &app{ctx: ctx, reg: reg, opts: opts, sched: runtime.NewScheduler(), palette: newPalette(reg), help: newHelp(), saga: newSagaOverlay()}
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
	return tea.Batch(a.refresh(), a.subscribeBus(), a.repaintTick())
}

// repaintTick drives 1-second UI repaints so the refresh countdown updates.
func (a *app) repaintTick() tea.Cmd {
	return tea.Tick(time.Second, func(_ time.Time) tea.Msg { return repaintMsg{} })
}

type repaintMsg struct{}

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
			a.lastRefresh = time.Now()
			a.nextRefresh = a.lastRefresh.Add(a.sched.Interval(a.resource.Name))
		}
		return a, a.scheduleRefresh()
	case tickMsg:
		return a, tea.Batch(a.refresh())
	case repaintMsg:
		return a, a.repaintTick()
	case toastExpireMsg:
		if a.toast != nil && time.Now().After(a.toast.until) {
			a.toast = nil
		}
		return a, nil
	case sagaEventMsg:
		ev := runtime.Event(msg)
		a.saga.Push(ev)
		if ev.Done {
			a.sched.Bump(ev.Resource)
			var toastCmd tea.Cmd
			if ev.Err != nil {
				toastCmd = a.showToast(toastErr, fmt.Sprintf("%s failed: %s", ev.Saga, ev.Err.Error()))
			} else {
				toastCmd = a.showToast(toastOK, fmt.Sprintf("%s succeeded", ev.Saga))
			}
			return a, tea.Batch(a.saga.DismissAfter(), a.refresh(), a.subscribeBus(), toastCmd)
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

// headerH is the height of the top block: banner(ascii.Height) + toast(1) + resource hints(1).
var headerH = ascii.Height + 2

func (a *app) View() tea.View {
	w, h := a.width, a.height
	if w < 40 || h < 12 {
		return tea.NewView("")
	}

	header := a.renderHeader(w)  // headerH rows
	bottom := a.renderBottom(w)  // 1 row: pills + key hints
	bodyH := h - headerH - 1
	if bodyH < 3 {
		bodyH = 3
	}
	body := a.renderBody(w, bodyH)

	base := lipgloss.JoinVertical(lipgloss.Left, header, body, bottom)

	var overlays []*lipgloss.Layer
	if a.showHelp {
		dim := lipgloss.NewStyle().Width(w).Height(h).Foreground(theme.Muted).Render("")
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

// renderHeader builds the top block:
//   rows 0-4: auto-generated ASCII banner for the CLI name (or user override).
//   row 5:    toast (or blank).
func (a *app) renderHeader(w int) string {
	banner := a.opts.Logo
	bannerLine := lipgloss.NewStyle().Width(w).Padding(0, 1).Render(banner)

	toastLine := ""
	if a.toast != nil {
		toastLine = lipgloss.PlaceHorizontal(w, lipgloss.Center, a.toast.view())
	}

	resHints := a.renderResourceHints(w)

	block := lipgloss.JoinVertical(lipgloss.Left, bannerLine, toastLine, resHints)
	return lipgloss.NewStyle().Width(w).Height(headerH).MaxHeight(headerH).Render(block)
}

// renderResourceHints lays out resource hotkeys horizontally across one row.
func (a *app) renderResourceHints(w int) string {
	all := a.reg.All()
	if len(all) == 0 {
		return ""
	}
	var parts []string
	for i, r := range all {
		if i >= 10 {
			break
		}
		key := theme.Key.Render(fmt.Sprintf("<%d>", i))
		name := r.Plural
		style := theme.MutedText
		if a.resource != nil && r.Name == a.resource.Name {
			style = theme.Value
		}
		parts = append(parts, key+" "+style.Render(name))
	}
	row := " " + strings.Join(parts, "   ")
	return lipgloss.NewStyle().Width(w).Render(row)
}

// renderBody draws a bordered box with a title embedded on the top border
// (like k9s's "Pods(all)[23]") and a refresh counter embedded on the bottom
// border. The border is hand-constructed so all four sides share exactly one
// BorderForeground color — no ANSI splicing.
func (a *app) renderBody(w, h int) string {
	title := "—"
	count := 0
	if a.resource != nil {
		title = a.resource.Plural
		count = len(a.items)
	}
	titleStr := theme.Heading.Render(title) + theme.MutedText.Render(fmt.Sprintf("[%d]", count))
	if a.mode == modeDetail && a.resource != nil && len(a.items) > 0 {
		id := detailID(a.resource, a.items[a.cursor])
		titleStr = theme.Heading.Render(a.resource.Name) + theme.MutedText.Render("/") + theme.Value.Render(id)
	}
	footerStr := theme.MutedText.Render(a.refreshFooter())

	innerW := w - 2 // minus left/right borders
	innerH := h - 2 // minus top/bottom borders
	if innerH < 1 {
		innerH = 1
	}

	inner := a.renderList(innerW, innerH)
	// Force inner to exactly innerW × innerH so the side borders line up.
	inner = lipgloss.NewStyle().Width(innerW).Height(innerH).MaxHeight(innerH).Render(inner)

	borderStyle := lipgloss.NewStyle().Foreground(theme.Subtle)
	top := borderLine(w, "╭", "─", "╮", titleStr, borderStyle)
	bottom := borderLine(w, "╰", "─", "╯", footerStr, borderStyle)

	// Prefix each inner line with a styled vertical bar.
	vert := borderStyle.Render("│")
	var lines []string
	lines = append(lines, top)
	for _, ln := range strings.Split(inner, "\n") {
		lines = append(lines, vert+ln+vert)
	}
	lines = append(lines, bottom)
	return strings.Join(lines, "\n")
}

// borderLine builds a horizontal border line of the given total width, with
// `label` centered in it. `left`, `mid`, and `right` are the corner/fill runes.
// All border runes (corners + fill) use the same style so the border appears as
// a single color.
func borderLine(width int, left, mid, right, label string, style lipgloss.Style) string {
	if width < 2 {
		return ""
	}
	midW := width - 2
	labelW := lipgloss.Width(label)
	if labelW > midW-2 {
		// Label too wide: clip to fit (best-effort).
		label = ""
		labelW = 0
	}
	leftPad := (midW - labelW) / 2
	rightPad := midW - labelW - leftPad
	line := style.Render(left) +
		style.Render(strings.Repeat(mid, leftPad)) +
		label +
		style.Render(strings.Repeat(mid, rightPad)) +
		style.Render(right)
	return line
}

// refreshFooter is the "Refresh in 10s | Last Refresh: 30s ago" string.
func (a *app) refreshFooter() string {
	if a.resource == nil || a.lastRefresh.IsZero() {
		return ""
	}
	now := time.Now()
	next := a.nextRefresh.Sub(now)
	since := now.Sub(a.lastRefresh)
	return fmt.Sprintf(" Refresh in %s │ Last Refresh: %s ago ", duration.Short(next), duration.Short(since))
}

func (a *app) renderList(maxW, maxH int) string {
	if a.resource == nil {
		return theme.MutedText.Render("no resources registered")
	}
	if a.err != nil {
		return theme.Err.Render(a.err.Error())
	}
	if a.mode == modeDetail && len(a.items) > 0 {
		return detailFor(a.resource, a.items[a.cursor])
	}
	return a.renderTable(maxH, maxW)
}

// renderTable builds a lipgloss table with headers and a highlighted cursor row.
func (a *app) renderTable(maxRows, maxW int) string {
	var headers []string
	var fields []registry.Field
	for _, f := range a.resource.Fields {
		if f.Table.Header == "" || f.Table.Wide {
			continue
		}
		headers = append(headers, f.Table.Header)
		fields = append(fields, f)
	}
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
		Width(maxW).
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

// renderBottom renders the last row: breadcrumb pills on the left,
// contextual key bindings on the right.
func (a *app) renderBottom(w int) string {
	var pills []string
	for _, r := range a.reg.All() {
		if a.resource != nil && r.Name == a.resource.Name {
			pills = append(pills, theme.PillActive.Render("<"+r.Name+">"))
		} else {
			pills = append(pills, theme.PillIdle.Render("<"+r.Name+">"))
		}
	}
	left := " " + strings.Join(pills, " ")

	keys := []string{
		theme.Key.Render("<:>") + " palette",
		theme.Key.Render("<?>") + " help",
		theme.Key.Render("<r>") + " refresh",
		theme.Key.Render("<↵>") + " detail",
		theme.Key.Render("<esc>") + " back",
		theme.Key.Render("<q>") + " quit",
	}
	right := strings.Join(keys, "  ") + " "

	lw := lipgloss.Width(left)
	rw := lipgloss.Width(right)
	fill := w - lw - rw
	if fill < 1 {
		fill = 1
	}
	return lipgloss.NewStyle().Width(w).Render(left + strings.Repeat(" ", fill) + right)
}

// dismissSagaMsg clears the saga overlay after the auto-dismiss timer fires.
type dismissSagaMsg struct{}
