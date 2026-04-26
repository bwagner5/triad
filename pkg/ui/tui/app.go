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
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"
	"github.com/bwagner5/triad/pkg/duration"
	"github.com/bwagner5/triad/pkg/registry"
	"github.com/bwagner5/triad/pkg/runtime"
	"github.com/bwagner5/triad/pkg/trace"
	"github.com/bwagner5/triad/pkg/ui/ascii"
	"github.com/bwagner5/triad/pkg/ui/theme"
)

func reflectIndirect(v any) reflect.Value                 { return reflect.Indirect(reflect.ValueOf(v)) }
func readField(rv reflect.Value, f registry.Field) string { return read(rv, f) }

// Options configures the TUI.
type Options struct {
	// Name is the CLI name shown as the banner at the top of the screen.
	Name string
	// Logo, if non-empty, overrides the auto-generated ASCII banner derived
	// from Name. Supply a pre-styled, multi-line string.
	Logo string
	// Version, if non-empty, is rendered centered in the bottom status bar.
	Version string
	// Context, if non-nil, is called on every repaint to produce a short
	// human-readable label for the current operating context (e.g. a region
	// name, account ID, kube context). Rendered in the bottom status bar
	// between the version and the key hints. Keep output to ~20 chars.
	Context func() string
	// GlobalOps are cross-cutting operations the TUI exposes via key
	// bindings + the command palette. They are not attached to any
	// resource; they typically change session state (region switch, auth
	// context, theme) that affects every resource.
	GlobalOps []registry.Operation
}

// Run starts the full-screen TUI against the given registry.
func Run(ctx context.Context, reg *registry.Registry, opts Options) error {
	trace.Log("tui.Run",
		"name", opts.Name, "resources", len(reg.All()),
		"globalOps", len(opts.GlobalOps), "hasContext", opts.Context != nil,
	)
	m := newApp(ctx, reg, opts)
	_, err := tea.NewProgram(m, tea.WithContext(ctx)).Run()
	trace.Log("tui.Run.exit", "err", err)
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
	bus   *runtime.Bus
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
	refreshing  bool
	initialLoad bool
	// streamGen monotonically increments on each refresh(). Inbound
	// itemsMsgs tagged with an older gen are ignored — prevents dup
	// items when a new refresh begins before the previous stream drains.
	streamGen int
	// seenGen is the gen whose first batch we've already applied (via
	// replace). Subsequent batches within the same gen append. Keeps the
	// old table visible during refresh until new data arrives.
	seenGen int
	spin    spinner.Model

	// Toast flash messages (top of screen).
	toast *toast

	palette paletteModel
	help    helpOverlay
	saga    sagaOverlay
	confirm confirmOverlay
	wizard  wizardOverlay

	// sagaProvide is set when a running saga emitted NeedsInput and the
	// wizard is collecting the missing fields. The next wizardDoneMsg
	// feeds the answers back to the runtime via this callback.
	sagaProvide func(registry.Input)
	// sagaPreInput snapshots the saga's State.Input at the moment it
	// paused for NeedsInput, so the confirm overlay can show everything
	// the step is about to run with — not just the fields the wizard
	// just collected.
	sagaPreInput registry.Input
	// sagaConfirmPrompt is the Confirm label to show before resuming a
	// paused saga. Derived from the NeedInput Reason (or a default).
	sagaConfirmPrompt string
	// pendingProvide / pendingMerged hold the saga-resume callback and
	// the merged input while the confirm overlay is showing. Populated
	// in handleWizardDone for paused sagas; drained by the confirm.
	pendingProvide func(registry.Input)
	pendingMerged  registry.Input
	// sagaCh holds the current saga's event channel so handleSagaEvent
	// can keep draining it via readSagaCmd after each event.
	sagaCh <-chan runtime.Event

	showPalette bool
	showHelp    bool

	// Table filter (activated by "/").
	filtering  bool
	filterText string
	filterTI   textinput.Model
}

func newApp(ctx context.Context, reg *registry.Registry, opts Options) *app {
	if opts.Name == "" {
		opts.Name = "cli"
	}
	if opts.Logo == "" {
		opts.Logo = lipgloss.NewStyle().Foreground(theme.Warning).Render(ascii.Render(opts.Name))
	}
	a := &app{ctx: ctx, reg: reg, opts: opts, sched: runtime.NewScheduler(), bus: runtime.NewBus(), palette: newPalette(reg, opts.GlobalOps), help: newHelp(), saga: newSagaOverlay(), wizard: newWizardOverlay(), spin: spinner.New(), initialLoad: true}
	fti := textinput.New()
	fti.Prompt = "/ "
	fti.Placeholder = "filter…"
	a.filterTI = fti
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
	streamed bool                  // true when this is a partial batch from StreamList (append, don't replace)
	final    bool                  // true when streaming is complete
	next     <-chan registry.Batch // when non-nil, caller should read another batch from it
	gen      int                   // stream generation; handleItems drops stale msgs
}
type sagaEventMsg runtime.Event

func (a *app) Init() tea.Cmd {
	return tea.Batch(a.refresh(), a.subscribeBus(), a.repaintTick(), a.spin.Tick)
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
	a.refreshing = true
	a.streamGen++
	gen := a.streamGen
	res := a.resource
	// If the Store is streaming-capable, consume its channel and emit one
	// itemsMsg per batch so the UI can render progressively. Otherwise
	// fall back to the blocking List.
	//
	// We do NOT reset a.items here — keep the old table visible until the
	// first batch of the new stream arrives, then replace. handleItems
	// tracks which gens it has seen a first batch for.
	if ss, ok := res.Store.(registry.StreamStore); ok {
		trace.Log("tui.refresh.stream", "resource", res.Name, "gen", gen)
		ch := ss.StreamList(a.ctx, registry.Filter{})
		return a.readStream(res.Name, ch, gen)
	}
	trace.Log("tui.refresh.list", "resource", res.Name, "gen", gen)
	return func() tea.Msg {
		items, err := res.Store.List(a.ctx, registry.Filter{})
		return itemsMsg{resource: res.Name, items: items, err: err, final: true, gen: gen}
	}
}

// readStream reads one batch off ch and posts it as an itemsMsg.
func (a *app) readStream(name string, ch <-chan registry.Batch, gen int) tea.Cmd {
	return func() tea.Msg {
		b, ok := <-ch
		if !ok {
			trace.Log("tui.stream.final", "resource", name, "gen", gen)
			return itemsMsg{resource: name, streamed: true, final: true, gen: gen}
		}
		trace.Log("tui.stream.batch", "resource", name, "items", len(b.Items), "err", b.Err, "gen", gen)
		return itemsMsg{resource: name, items: b.Items, err: b.Err, streamed: true, next: ch, gen: gen}
	}
}

// handleItems applies an incoming itemsMsg (streaming or blocking). Split
// out of Update to keep gocyclo happy.
func (a *app) handleItems(msg itemsMsg) (tea.Model, tea.Cmd) {
	if a.resource == nil || msg.resource != a.resource.Name {
		return a, nil
	}
	// Drop stale messages from a prior refresh cycle — otherwise a batch
	// in flight when refresh() is called again would append to the fresh
	// list and produce duplicates.
	if msg.gen != a.streamGen {
		trace.Log("tui.items.stale", "gen", msg.gen, "current", a.streamGen)
		return a, nil
	}
	if !msg.streamed {
		a.items, a.err = msg.items, msg.err
		a.refreshing = false
		a.initialLoad = false
		if a.cursor >= len(a.items) {
			a.cursor = 0
		}
		a.lastRefresh = time.Now()
		a.nextRefresh = a.lastRefresh.Add(a.sched.Interval(a.resource.Name))
		return a, a.scheduleRefresh()
	}
	// Streaming: first batch of a new gen replaces; subsequent batches
	// append. Keeps the old table stable during refresh — rows disappear
	// only when they've been superseded by fresh data.
	if msg.err != nil {
		cmds := []tea.Cmd{a.showToast(toastErr, msg.err.Error())}
		if msg.next != nil {
			cmds = append(cmds, a.readStream(msg.resource, msg.next, msg.gen))
		}
		return a, tea.Batch(cmds...)
	}
	if len(msg.items) > 0 {
		if msg.gen != a.seenGen {
			// First batch of a fresh cycle: replace the old table.
			a.items = append(a.items[:0], msg.items...)
			a.seenGen = msg.gen
		} else {
			a.items = append(a.items, msg.items...)
		}
	} else if msg.final && msg.gen != a.seenGen {
		// Edge case: stream produced no batches at all (empty result).
		// Drop the old table now rather than leave stale rows.
		a.items = a.items[:0]
		a.seenGen = msg.gen
	}
	if msg.final {
		a.refreshing = false
		a.initialLoad = false
		a.lastRefresh = time.Now()
		a.nextRefresh = a.lastRefresh.Add(a.sched.Interval(a.resource.Name))
		return a, a.scheduleRefresh()
	}
	if msg.next != nil {
		return a, a.readStream(msg.resource, msg.next, msg.gen)
	}
	return a, nil
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
		a.wizard.SetSize(msg.Width, msg.Height)
	case tea.KeyPressMsg:
		return a.handleKey(msg)
	case itemsMsg:
		return a.handleItems(msg)
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
		return a.handleSagaEvent(runtime.Event(msg))
	case paletteResultMsg:
		a.showPalette = false
		return a.handlePaletteChoice(msg)
	case startSagaMsg:
		return a, a.startSaga(msg)
	case wizardDoneMsg:
		return a.handleWizardDone(msg)
	case wizardSuggestMsg:
		if a.wizard.Active() {
			a.wizard.Update(msg)
		}
		return a, nil
	case actionDoneMsg:
		if msg.err != nil {
			return a, a.showToast(toastErr, msg.err.Error())
		}
		return a, a.refresh()
	case dismissSagaMsg:
		a.saga.Clear()
		a.sagaCh = nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		a.spin, cmd = a.spin.Update(msg)
		return a, cmd
	}
	// Route unhandled messages to wizard overlay when active (e.g. spinner ticks).
	if a.wizard.Active() {
		if _, cmd := a.wizard.Update(msg); cmd != nil {
			return a, cmd
		}
	}
	if a.filtering {
		var cmd tea.Cmd
		a.filterTI, cmd = a.filterTI.Update(msg)
		return a, cmd
	}
	return a, nil
}

func (a *app) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// Confirm overlay takes priority over everything.
	if a.confirm.Active() {
		return a.handleConfirmKey(key)
	}

	// Wizard overlay.
	if a.wizard.Active() {
		consumed, cmd := a.wizard.Update(msg)
		if consumed {
			return a, cmd
		}
		return a, nil
	}

	// Saga overlay: esc always dismisses; enter dismisses when in terminal state.
	if a.saga.Active() {
		if key == "esc" || (a.saga.done && key == "enter") {
			a.saga.Clear()
			return a, nil
		}
		return a, nil // consume all keys while saga is active
	}

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

	// Always-on emergency exit.
	if key == "ctrl+c" {
		return a, tea.Quit
	}

	// Filter mode: text input active.
	if a.filtering {
		return a.handleFilterKey(msg)
	}

	// Arrow-key aliases for j/k navigation.
	switch key {
	case "up":
		key = "k"
	case "down":
		key = "j"
	}

	// Numeric resource switching: 0..9 jumps to that registered resource.
	if len(key) == 1 && key[0] >= '0' && key[0] <= '9' {
		idx := int(key[0] - '0')
		all := a.reg.All()
		if idx < len(all) {
			a.resource = &all[idx]
			a.mode = modeList
			a.cursor = 0
			a.filterText = ""
			return a, a.refresh()
		}
	}

	if m, cmd, ok := a.dispatch(key); ok {
		return m, cmd
	}
	return a, nil
}

func (a *app) handleWizardDone(msg wizardDoneMsg) (tea.Model, tea.Cmd) {
	trace.Log("tui.wizardDone", "op", msg.op.Name, "hasRun", msg.op.Run != nil, "hasSteps", len(msg.op.Steps) > 0, "hasSagaProvide", a.sagaProvide != nil, "canceled", msg.input == nil)
	// If a saga is paused waiting for input, route through the confirm
	// overlay so the user reviews the merged inputs (pre-existing State +
	// new wizard answers) before the saga resumes. Cancel passes nil to
	// Provide, which aborts.
	if a.sagaProvide != nil {
		provide := a.sagaProvide
		preInput := a.sagaPreInput
		prompt := a.sagaConfirmPrompt
		a.sagaProvide = nil
		a.sagaPreInput = nil
		a.sagaConfirmPrompt = ""
		if msg.input == nil {
			// User canceled the wizard — abort the saga. Drain the
			// channel so its post-abort events arrive.
			abortCmd := func() tea.Msg { provide(nil); return nil }
			if a.sagaCh != nil {
				return a, tea.Batch(abortCmd, readSagaCmd(a.sagaCh))
			}
			return a, abortCmd
		}
		merged := registry.Input{}
		for k, v := range preInput {
			merged[k] = v
		}
		for k, v := range msg.input {
			merged[k] = v
		}
		// Route through confirm. On Yes, startSagaMsg won't fire (we have
		// no new saga); instead we reuse the confirm overlay with a
		// synthetic op whose Fields cover the merged keys, and route the
		// confirm decision back to Provide via a.pendingProvide.
		a.pendingProvide = provide
		a.pendingMerged = merged
		trace.Log("tui.confirm.armed", "fields", len(merged))
		// Close the wizard so the confirm overlay has the stage.
		a.wizard.Clear()
		a.confirm.Show(prompt, nil, sagaNeedConfirmOp(merged), merged)
		return a, nil
	}
	// Non-saga cancel: drop on the floor.
	if msg.input == nil {
		return a, nil
	}
	if msg.op != nil && msg.op.Confirm != "" {
		a.wizard.Clear()
		a.confirm.Show(msg.op.Confirm, msg.resource, msg.op, msg.input)
		return a, nil
	}
	// Run op collected via the wizard overlay: invoke Run in-process and
	// synthesize an actionDoneMsg so the TUI refreshes + surfaces errors.
	if msg.op != nil && msg.op.Run != nil && len(msg.op.Steps) == 0 {
		op := msg.op
		input := msg.input
		return a, func() tea.Msg {
			err := op.Run(a.ctx, input)
			trace.Log("tui.wizardDone.run", "op", op.Name, "err", err)
			return actionDoneMsg{err: err}
		}
	}
	return a, func() tea.Msg {
		return startSagaMsg{resource: msg.resource, op: msg.op, input: msg.input}
	}
}

func (a *app) handleFilterKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		a.filtering = false
		a.filterText = ""
		a.filterTI.SetValue("")
		a.filterTI.Blur()
		a.cursor = 0
		return a, nil
	case "enter":
		a.filtering = false
		a.filterText = a.filterTI.Value()
		a.filterTI.Blur()
		a.cursor = 0
		return a, nil
	}
	var cmd tea.Cmd
	a.filterTI, cmd = a.filterTI.Update(msg)
	a.filterText = a.filterTI.Value()
	a.cursor = 0
	return a, cmd
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
		a.filterText = ""
		return a, a.refresh()
	case paletteGlobal:
		return a, a.launchOp(nil, e.op, nil)
	}
	return a, nil
}

func (a *app) handleSagaEvent(ev runtime.Event) (tea.Model, tea.Cmd) {
	// Always keep draining the saga channel so the runtime goroutine
	// isn't blocked on a full buffer. subscribeBus covers cross-component
	// signaling (refresh-on-done); readSagaCmd covers the per-step flow.
	drain := func() tea.Cmd {
		if a.sagaCh != nil {
			return readSagaCmd(a.sagaCh)
		}
		return a.subscribeBus()
	}

	if ev.Status == runtime.NeedsInput && ev.Needs != nil && ev.Provide != nil {
		trace.Log("tui.saga.needsInput", "step", ev.Step, "fields", len(ev.Needs.Fields))
		a.sagaProvide = ev.Provide
		a.sagaPreInput = ev.State
		a.sagaConfirmPrompt = ev.Needs.Reason
		if a.sagaConfirmPrompt == "" {
			a.sagaConfirmPrompt = "Ready to continue?"
		}
		input := registry.Input{}
		return a, tea.Batch(
			a.wizard.Show(a.ctx, nil, sagaNeedOp(ev.Needs), ev.Needs.Fields, input),
			drain(),
		)
	}
	a.saga.Push(ev)
	if ev.Done {
		a.sched.Bump(ev.Resource)
		a.sagaCh = nil
		var toastCmd tea.Cmd
		if ev.Err != nil {
			toastCmd = a.showToast(toastErr, fmt.Sprintf("%s failed: %s", ev.Saga, ev.Err.Error()))
		} else {
			toastCmd = a.showToast(toastOK, fmt.Sprintf("%s succeeded", ev.Saga))
		}
		return a, tea.Batch(a.saga.DismissAfter(), a.refresh(), a.subscribeBus(), toastCmd)
	}
	return a, drain()
}

// handleConfirmKey routes a key into the active confirm overlay and
// dispatches Yes/No to either the saga-resume Provide callback (paused
// saga) or the startSaga path (fresh Confirm-gated op).
func (a *app) handleConfirmKey(key string) (tea.Model, tea.Cmd) {
	accepted, confirmed := a.confirm.HandleKey(key)
	trace.Log("tui.confirm.key", "key", key, "accepted", accepted, "confirmed", confirmed, "pendingProvide", a.pendingProvide != nil)
	if !accepted {
		return a, nil
	}
	// Case 1: saga-resume confirm (pendingProvide set).
	if a.pendingProvide != nil {
		provide := a.pendingProvide
		merged := a.pendingMerged
		a.pendingProvide = nil
		a.pendingMerged = nil
		if confirmed {
			// Deliver answers to the runtime, then keep draining the saga
			// channel. Without a subsequent readSagaCmd, the runtime's
			// post-resume events would fill the buffer and stall with no
			// reader to wake things up — i.e. 'I said yes and nothing
			// happened'.
			provideCmd := func() tea.Msg { provide(merged); return nil }
			if a.sagaCh != nil {
				return a, tea.Batch(provideCmd, readSagaCmd(a.sagaCh))
			}
			return a, provideCmd
		}
		// Abort: tell the runtime we're not providing input. Still need
		// to drain so its post-abort Failed/Done events arrive.
		abortCmd := func() tea.Msg { provide(nil); return nil }
		if a.sagaCh != nil {
			return a, tea.Batch(abortCmd, readSagaCmd(a.sagaCh))
		}
		return a, abortCmd
	}
	// Case 2: fresh-saga confirm (Operation.Confirm).
	if confirmed {
		return a, func() tea.Msg {
			return startSagaMsg{
				resource: a.confirm.resource,
				op:       a.confirm.op,
				input:    a.confirm.input,
			}
		}
	}
	return a, nil
}

// sagaNeedOp is a synthetic Operation just so the wizard overlay has a
// name to render for a NeedsInput prompt. It is never executed.
func sagaNeedOp(n *registry.NeedInput) *registry.Operation {
	name := "additional input"
	if n.Reason != "" {
		name = n.Reason
	}
	return &registry.Operation{Name: name, Fields: n.Fields}
}

// sagaNeedConfirmOp builds a synthetic Operation whose Fields list every
// key in merged so the confirm overlay's summary can render each one.
// Sorted for stable output. Never executed — just feeds the summary.
func sagaNeedConfirmOp(merged registry.Input) *registry.Operation {
	keys := make([]string, 0, len(merged))
	for k := range merged {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fields := make([]registry.Field, 0, len(keys))
	for _, k := range keys {
		fields = append(fields, registry.Field{Flag: k})
	}
	return &registry.Operation{Name: "confirm", Fields: fields}
}

// subscribeBus relays saga events from the app's bus back into the TUI.
func (a *app) subscribeBus() tea.Cmd {
	ch := a.bus.Subscribe()
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

	header := a.renderHeader(w) // headerH rows
	bottom := a.renderBottom(w) // 1 row: pills + key hints
	bodyH := h - headerH - 1
	if bodyH < 3 {
		bodyH = 3
	}
	body := a.renderBody(w, bodyH)

	base := lipgloss.JoinVertical(lipgloss.Left, header, body, bottom)

	overlayActive := a.showHelp || a.showPalette || a.saga.Active() || a.confirm.Active() || a.wizard.Active()
	rootContent := base
	if overlayActive {
		rootContent = dimBackground(base)
	}

	var overlays []*lipgloss.Layer
	if a.showHelp {
		overlays = append(overlays, centeredLayer(a.help.Box(w, h, a.keyMap()), w, h, 2))
	}
	if a.showPalette {
		overlays = append(overlays, centeredLayer(a.palette.Box(w, h), w, h, 3))
	}
	if a.saga.Active() {
		overlays = append(overlays, centeredLayer(a.saga.Box(w, h, a.spin.View()), w, h, 4))
	}
	if a.confirm.Active() {
		overlays = append(overlays, centeredLayer(a.confirm.Box(w, h), w, h, 5))
	}
	if a.wizard.Active() {
		// Wizard is full-screen-ish; render it centered and large.
		overlays = append(overlays, centeredLayer(a.wizard.Box(w, h), w, h, 5))
	}

	root := lipgloss.NewLayer(rootContent)
	if len(overlays) > 0 {
		root = root.AddLayers(overlays...)
	}
	v := tea.NewView(lipgloss.NewCompositor(root).Render())
	v.AltScreen = true
	return v
}

// dimBackground strips ANSI styling from s and re-renders every non-space
// cell in a muted color, producing a clearly darker-than-foreground backdrop
// behind modal overlays.
func dimBackground(s string) string {
	plain := stripANSI(s)
	return lipgloss.NewStyle().Foreground(theme.Muted).Render(plain)
}

// stripANSI removes CSI-SGR escape sequences from s.
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
//
//	rows 0-4: auto-generated ASCII banner for the CLI name (or user override).
//	row 5:    toast (or blank).
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
	if a.filtering {
		titleStr += "  " + a.filterTI.View()
	} else if a.filterText != "" {
		titleStr += "  " + theme.MutedText.Render("/"+a.filterText)
	}
	if a.mode == modeDetail && a.resource != nil && len(a.items) > 0 {
		id := detailID(a.resource, a.items[a.cursor])
		titleStr = theme.Heading.Render(a.resource.Name) + theme.MutedText.Render("/") + theme.Value.Render(id)
	}
	footerStr := a.refreshFooter()

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
	if a.refreshing {
		return " Refreshing " + a.spin.View() + " "
	}
	if a.resource == nil || a.lastRefresh.IsZero() {
		return ""
	}
	now := time.Now()
	next := a.nextRefresh.Sub(now)
	since := now.Sub(a.lastRefresh)
	return theme.MutedText.Render(fmt.Sprintf(" Refresh in %s │ Last Refresh: %s ago ", duration.Short(next), duration.Short(since)))
}

func (a *app) filteredItems() []any {
	if a.filterText == "" {
		return a.items
	}
	q := strings.ToLower(a.filterText)
	var out []any
	for _, item := range a.items {
		rv := reflectIndirect(item)
		for _, f := range a.resource.Fields {
			if f.Table.Header == "" {
				continue
			}
			if strings.Contains(strings.ToLower(readField(rv, f)), q) {
				out = append(out, item)
				break
			}
		}
	}
	return out
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
	if len(a.items) == 0 && a.initialLoad {
		return lipgloss.Place(maxW, maxH, lipgloss.Center, lipgloss.Center,
			a.spin.View()+"  "+theme.MutedText.Render("Loading "+a.resource.Plural+"…"))
	}
	if len(a.items) == 0 {
		return theme.MutedText.Render(fmt.Sprintf("No %s to display…", a.resource.Plural))
	}
	filtered := a.filteredItems()
	if len(filtered) == 0 {
		return theme.MutedText.Render(fmt.Sprintf("No %s matching %q", a.resource.Plural, a.filterText))
	}
	return a.renderTable(filtered, maxH, maxW)
}

// renderTable builds a lipgloss table with headers and a highlighted cursor row.
func (a *app) renderTable(items []any, maxRows, maxW int) string {
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
	if end > len(items) {
		end = len(items)
	}
	var rows [][]string
	cursorRow := -1
	for i := start; i < end; i++ {
		rv := reflectIndirect(items[i])
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
			switch row {
			case table.HeaderRow:
				return theme.Label.PaddingRight(2)
			case cursorRow:
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

	// Build key hints from the dynamic key map, keeping resource + global
	// bindings on the status bar (Navigation is implicit / too numerous).
	var keys []string
	for _, b := range a.keyMap() {
		if b.Cat == "Navigation" {
			continue
		}
		keys = append(keys, theme.Key.Render("<"+b.Key+">")+" "+b.Label)
	}
	right := strings.Join(keys, "  ") + " "

	lw := lipgloss.Width(left)
	rw := lipgloss.Width(right)
	// Combine version + (optional) context into a single centered label so
	// spacing stays predictable whether Context is set or not.
	centerLabel := ""
	if a.opts.Version != "" {
		centerLabel = theme.MutedText.Render(a.opts.Version)
	}
	if a.opts.Context != nil {
		if ctx := a.opts.Context(); ctx != "" {
			dot := theme.MutedText.Render(" · ")
			pill := theme.PillActive.Render(ctx)
			if centerLabel != "" {
				centerLabel = centerLabel + dot + pill
			} else {
				centerLabel = pill
			}
		}
	}
	vw := lipgloss.Width(centerLabel)
	// Center the label in the full width, then pad between it and the
	// left/right chunks. If the right side overlaps the centered label,
	// fall back to left-of-right placement.
	leftFill := (w-vw)/2 - lw
	rightFill := w - lw - leftFill - vw - rw
	if leftFill < 1 || rightFill < 1 {
		leftFill = 1
		rightFill = w - lw - leftFill - vw - rw
		if rightFill < 1 {
			rightFill = 1
		}
	}
	return lipgloss.NewStyle().Width(w).Render(left + strings.Repeat(" ", leftFill) + centerLabel + strings.Repeat(" ", rightFill) + right)
}

// dismissSagaMsg clears the saga overlay after the auto-dismiss timer fires.
type dismissSagaMsg struct{}
