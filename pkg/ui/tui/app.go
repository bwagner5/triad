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
	"github.com/bwagner5/triad/pkg/ui/wizardstate"
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
	ctx = trace.WithUI(ctx, "tui")
	log := trace.FromContext(ctx)
	log.InfoContext(ctx, "tui start",
		"name", opts.Name, "resources", len(reg.All()),
		"globalOps", len(opts.GlobalOps), "hasContext", opts.Context != nil,
	)
	m := newApp(ctx, reg, opts)
	_, err := tea.NewProgram(m, tea.WithContext(ctx)).Run()
	log.InfoContext(ctx, "tui exit", "err", err)
	return err
}

// RunWith is like Run but starts with the wizard overlay already active,
// pre-populated from a wizardstate.State that the inline CLI wizard
// handed off via Ctrl+T. The TUI shows the form, and on submit runs
// the saga (or Run op) inside the TUI.
func RunWith(ctx context.Context, reg *registry.Registry, opts Options, res *registry.Resource, op *registry.Operation, state *wizardstate.State) error {
	ctx = trace.WithUI(ctx, "tui")
	log := trace.FromContext(ctx)
	log.InfoContext(ctx, "tui start (handoff)",
		"name", opts.Name, "op", op.Name,
	)
	m := newApp(ctx, reg, opts)
	m.preloadResource = res
	m.preloadOp = op
	m.preloadState = state
	_, err := tea.NewProgram(m, tea.WithContext(ctx)).Run()
	log.InfoContext(ctx, "tui exit", "err", err)
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
	detailItem    any // enriched item for detail view (fetched via Get)

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
	// spin drives per-step spinners in the saga overlay and
	// async-field cells. Uses the default Line spinner (|/-\).
	spin spinner.Model
	// spinCategory drives spinners on category headers in the saga
	// overlay. Uses Pulse (█▓▒░) so it's visually distinct from
	// the step-level spinner — two identical spinners side by side
	// is distracting.
	spinCategory spinner.Model

	// async is the store for Field.Async results, keyed by
	// (resource, primary key, field name). Loaders run in goroutines
	// spawned after each refresh; the TUI renders the spinner for
	// cells whose entry is loading-and-never-succeeded, and shows the
	// last known value for cells that have succeeded at least once
	// (even across refreshes) — the column never flashes back to the
	// spinner once data has been seen.
	async map[asyncKey]*asyncState

	// Toast flash messages (top of screen).
	toast *toast

	palette paletteModel
	help    helpOverlay
	saga    sagaOverlay
	confirm confirmOverlay
	wizard  wizardOverlay
	review  reviewOverlay

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

	// Handoff preload from CLI wizard via Ctrl+T. When non-nil at
	// Init time, the wizard overlay opens immediately seeded with
	// the partial state, and on submit the corresponding op runs.
	preloadResource *registry.Resource
	preloadOp       *registry.Operation
	preloadState    *wizardstate.State
}

func newApp(ctx context.Context, reg *registry.Registry, opts Options) *app {
	if opts.Name == "" {
		opts.Name = "cli"
	}
	if opts.Logo == "" {
		opts.Logo = lipgloss.NewStyle().Foreground(theme.Warning).Render(ascii.Render(opts.Name))
	}
	a := &app{ctx: ctx, reg: reg, opts: opts, sched: runtime.NewScheduler(), bus: runtime.NewBus(), palette: newPalette(reg, opts.GlobalOps), help: newHelp(), saga: newSagaOverlay(), wizard: newWizardOverlay(), spin: spinner.New(), spinCategory: spinner.New(spinner.WithSpinner(spinner.Pulse)), initialLoad: true, async: map[asyncKey]*asyncState{}}
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
	replace  bool                  // true when items should merge-by-primary-key into the existing table (Batch.Replace)
	final    bool                  // true when streaming is complete
	next     <-chan registry.Batch // when non-nil, caller should read another batch from it
	gen      int                   // stream generation; handleItems drops stale msgs
}

// asyncKey identifies one async-field cell (resource, row-PK, field-name).
// Used as a map key for the async-load state so callers can update an
// individual cell without touching the rest of the row.
type asyncKey struct {
	resource string
	pk       string
	field    string
}

// asyncState is the per-cell state for a Field.Async loader.
//
// Lifecycle per cell:
//   - fresh cell, never loaded: loading=true, loaded=false — cell shows
//     the spinner.
//   - loader succeeded at least once: loaded=true, value set — cell
//     shows value. A subsequent refresh flips loading=true transiently
//     but the cell keeps rendering the old value (no flash).
//   - loader returned error: loaded=true, err set — cell shows "—".
type asyncState struct {
	value   string
	loading bool
	loaded  bool
	err     error
}

// asyncFieldMsg is emitted by an async-field loader goroutine when its
// Async callback returns. The TUI merges it into a.async.
type asyncFieldMsg struct {
	key   asyncKey
	value string
	err   error
}
type sagaEventMsg runtime.Event

func (a *app) Init() tea.Cmd {
	cmds := []tea.Cmd{a.refresh(), a.subscribeBus(), a.repaintTick(), a.spin.Tick, a.spinCategory.Tick}
	// Handoff path: if RunWith installed a preloaded wizard state,
	// open the overlay immediately so the user lands in the same
	// form they were filling out in the CLI.
	if a.preloadState != nil && a.preloadOp != nil {
		fields := a.preloadOp.Fields
		input := a.preloadState.LiveInput()
		cmds = append(cmds, a.wizard.ShowWithState(a.ctx, a.preloadResource, a.preloadOp, fields, input, a.preloadState, ""))
		a.preloadState = nil
	}
	return tea.Batch(cmds...)
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
		trace.Trace(a.ctx, "tui refresh stream", "resource", res.Name, "gen", gen)
		ch := ss.StreamList(a.ctx, registry.Filter{})
		return a.readStream(res.Name, ch, gen)
	}
	trace.Trace(a.ctx, "tui refresh list", "resource", res.Name, "gen", gen)
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
			trace.Trace(a.ctx, "tui stream final", "resource", name, "gen", gen)
			return itemsMsg{resource: name, streamed: true, final: true, gen: gen}
		}
		trace.Trace(a.ctx, "tui stream batch", "resource", name, "items", len(b.Items), "err", b.Err, "replace", b.Replace, "gen", gen)
		return itemsMsg{resource: name, items: b.Items, err: b.Err, streamed: true, replace: b.Replace, next: ch, gen: gen}
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
		trace.Trace(a.ctx, "tui items stale", "gen", msg.gen, "current", a.streamGen)
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
		return a, tea.Batch(append(a.scheduleAsyncLoads(), a.scheduleRefresh())...)
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
		switch {
		case msg.replace:
			// Merge-by-primary-key: Replace batches update the Status /
			// Endpoints / etc. columns of rows already shipped via an
			// earlier bare batch in the same gen. Matching is by the
			// first Field of the resource (convention: "Name"). Items
			// with no match fall through to append so a late-arriving
			// row (e.g. a region that finished after its bare batch was
			// expected) isn't silently dropped.
			a.items = mergeByPrimaryKey(a.items, msg.items, a.resource)
			// Don't touch a.seenGen: a Replace batch is an update, not
			// the first shipment of a new gen. seenGen flips when a
			// non-Replace batch arrives (the bare ship).
		case msg.gen != a.seenGen:
			// First batch of a fresh cycle: replace the old table.
			a.items = append(a.items[:0], msg.items...)
			a.seenGen = msg.gen
		default:
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
		return a, tea.Batch(append(a.scheduleAsyncLoads(), a.scheduleRefresh())...)
	}
	if msg.next != nil {
		return a, a.readStream(msg.resource, msg.next, msg.gen)
	}
	return a, nil
}

// scheduleAsyncLoads returns a batch of tea.Cmds — one per (row, async
// field) in the current table whose loader isn't already in flight. Each
// cmd runs Async in the bubbletea worker pool; on completion it emits an
// asyncFieldMsg back into Update.
//
// Safe to call on every table update (refresh, replace-merge, tick): the
// "already loading" guard ensures we don't pile up N-per-refresh loaders
// for the same cell. Cells whose loader previously failed get re-tried
// on subsequent refreshes — that's why loaded=true + err != nil doesn't
// inhibit scheduling.
func (a *app) scheduleAsyncLoads() []tea.Cmd {
	if a.resource == nil || len(a.items) == 0 {
		return nil
	}
	pk := primaryKeyField(a.resource)
	if pk == nil {
		return nil
	}
	// Collect async fields once per call — most resources have <= a
	// handful and the slice is small.
	var asyncFields []registry.Field
	for _, f := range a.resource.Fields {
		if f.Async != nil {
			asyncFields = append(asyncFields, f)
		}
	}
	if len(asyncFields) == 0 {
		return nil
	}
	var cmds []tea.Cmd
	for _, item := range a.items {
		pkVal := readField(reflectIndirect(item), *pk)
		if pkVal == "" {
			continue // no stable identity → skip async for this row
		}
		for _, f := range asyncFields {
			k := asyncKey{resource: a.resource.Name, pk: pkVal, field: f.Name}
			st, ok := a.async[k]
			if !ok {
				st = &asyncState{loading: true}
				a.async[k] = st
			} else if st.loading {
				continue // already in flight; let it finish
			} else {
				st.loading = true
			}
			// Capture loop vars.
			load := f.Async
			key := k
			row := item
			cmds = append(cmds, func() tea.Msg {
				v, err := load(a.ctx, row)
				return asyncFieldMsg{key: key, value: v, err: err}
			})
		}
	}
	return cmds
}

// renderAsyncCell returns the cell string for an async field. The rules:
//
//   - Have a successful value: show it. Subsequent loads never replace it
//     until they succeed — so a slow refresh doesn't flash the cell back
//     to the spinner.
//   - Loader errored and we have no prior value: show a muted "—".
//   - Loader is in flight (or never started, shouldn't happen post-
//     scheduleAsyncLoads): show the animated spinner.
func (a *app) renderAsyncCell(pk string, f registry.Field) string {
	st := a.async[asyncKey{resource: a.resource.Name, pk: pk, field: f.Name}]
	switch {
	case st == nil:
		// Loader hasn't been scheduled yet (window between new items
		// arriving and scheduleAsyncLoads running). Same treatment as
		// "in flight" — show the spinner so the cell is never blank.
		return a.spin.View()
	case st.value != "":
		return st.value
	case st.err != nil:
		return theme.MutedText.Render("—")
	default:
		return a.spin.View()
	}
}

// handleAsyncField merges an async-loader result back into a.async.
func (a *app) handleAsyncField(msg asyncFieldMsg) (tea.Model, tea.Cmd) {
	st, ok := a.async[msg.key]
	if !ok {
		st = &asyncState{}
		a.async[msg.key] = st
	}
	st.loading = false
	st.loaded = true
	st.err = msg.err
	if msg.err == nil {
		st.value = msg.value
	} else {
		trace.Trace(a.ctx, "async field load failed",
			"resource", msg.key.resource, "pk", msg.key.pk, "field", msg.key.field, "err", msg.err)
	}
	return a, nil
}

// primaryKeyField returns the Resource field used as the row identity for
// Replace-style merges. The convention is "first declared Field" — every
// Resource puts its name-ish primary column first (App.Name, Instance.Name,
// etc.). Returns nil if the resource has no fields (degenerate; caller
// falls back to append semantics).
func primaryKeyField(res *registry.Resource) *registry.Field {
	if res == nil || len(res.Fields) == 0 {
		return nil
	}
	return &res.Fields[0]
}

// mergeByPrimaryKey returns a slice where each item in updates has replaced
// the matching-by-primary-key entry in existing; items with no match are
// appended. Order of existing is preserved; unmatched updates are appended
// in the order they appear. Used by Replace batches in handleItems.
func mergeByPrimaryKey(existing, updates []any, res *registry.Resource) []any {
	pk := primaryKeyField(res)
	if pk == nil {
		// Nothing to match on — fall back to append so data isn't lost.
		return append(existing, updates...)
	}
	idx := make(map[string]int, len(existing))
	for i, it := range existing {
		idx[readField(reflectIndirect(it), *pk)] = i
	}
	out := append([]any(nil), existing...)
	for _, u := range updates {
		key := readField(reflectIndirect(u), *pk)
		if i, ok := idx[key]; ok {
			out[i] = u
			continue
		}
		idx[key] = len(out)
		out = append(out, u)
	}
	return out
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
		a.review.SetSize(msg.Width, msg.Height)
	case tea.KeyPressMsg:
		return a.handleKey(msg)
	case itemsMsg:
		return a.handleItems(msg)
	case tickMsg:
		cmds := []tea.Cmd{a.refresh()}
		// Keep the detail pane in sync with the periodic list
		// refresh. Without this, a user parked on a detail view sees
		// frozen deploy/container state even though the list under
		// it is being refreshed every interval.
		if a.mode == modeDetail {
			if cmd := a.fetchDetail(); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		return a, tea.Batch(cmds...)
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
		// Same rationale as the saga-done path: refresh repopulates
		// the table, but the detail pane's a.detailItem has to be
		// re-fetched separately.
		if a.mode == modeDetail {
			if cmd := a.fetchDetail(); cmd != nil {
				return a, tea.Batch(a.refresh(), cmd)
			}
		}
		return a, a.refresh()
	case dismissSagaMsg:
		// Auto-dismiss only fires when the user is looking at the
		// expanded overlay. If they minimized to the pill, leave the
		// DONE/ERROR badge in place until they manually dismiss it —
		// silently clearing a backgrounded saga would erase the only
		// surface they have to learn the outcome.
		if a.saga.Minimized() {
			return a, nil
		}
		a.saga.Clear()
		a.sagaCh = nil
	case detailFetchedMsg:
		if msg.item != nil && a.mode == modeDetail {
			a.detailItem = msg.item
		}
		return a, nil
	case spinner.TickMsg:
		// Each spinner filters ticks by ID internally, so it's safe
		// (and necessary) to forward every TickMsg to both spinners —
		// only the matching one actually advances.
		var cmd, cmd2 tea.Cmd
		a.spin, cmd = a.spin.Update(msg)
		a.spinCategory, cmd2 = a.spinCategory.Update(msg)
		return a, tea.Batch(cmd, cmd2)
	case asyncFieldMsg:
		return a.handleAsyncField(msg)
	}
	// Route unhandled messages to review overlay when active.
	if a.review.Active() {
		if _, cmd := a.review.Update(msg); cmd != nil {
			return a, cmd
		}
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

	// Review overlay (scrollable policy viewer) takes top priority.
	if a.review.Active() {
		consumed, cmd := a.review.Update(msg)
		if consumed {
			return a, cmd
		}
	}

	// Confirm overlay takes priority over everything else.
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

	// Saga overlay: j/k/enter navigate categories; esc dismisses;
	// enter dismisses when the saga finished and isn't sitting on a
	// category header (a toggleable cursor consumes enter first).
	// `-` toggles minimize at any time the saga is active. Other keys
	// fall through to the normal handler when minimized so the user
	// can still navigate the table while a saga runs in the corner.
	if a.saga.Active() {
		if key == "-" {
			a.saga.ToggleMinimize()
			return a, nil
		}
		if !a.saga.Minimized() {
			return a.handleSagaOverlayKey(key)
		}
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

	// Numeric resource switching: "1".."9" jumps to the 1st..9th
	// registered resource; "0" jumps to the 10th. Using 1-based keys
	// keeps the mapping in the same order the user reads the
	// breadcrumb pills (leftmost pill is "1") and avoids the awkward
	// layout where "0" sits on the far right of the number row but
	// would select the leftmost tab.
	if len(key) == 1 && key[0] >= '0' && key[0] <= '9' {
		idx := int(key[0] - '0')
		if idx == 0 {
			idx = 10
		}
		idx-- // convert to 0-based slice index
		all := a.reg.All()
		if idx < len(all) {
			a.resource = &all[idx]
			a.mode = modeList
			a.cursor = 0
			a.filterText = ""
			a.items = nil
			a.initialLoad = true
			return a, a.refresh()
		}
	}

	if m, cmd, ok := a.dispatch(key); ok {
		return m, cmd
	}
	return a, nil
}

func (a *app) handleWizardDone(msg wizardDoneMsg) (tea.Model, tea.Cmd) {
	trace.Trace(a.ctx, "tui wizard done", "op", msg.op.Name, "hasRun", msg.op.Run != nil, "hasSteps", len(msg.op.Steps) > 0, "hasSagaProvide", a.sagaProvide != nil, "canceled", msg.input == nil)
	// If a saga is paused waiting for input, the wizard already showed
	// the NeedInput.Reason as a preamble and collected the fields.
	// Provide the merged input directly back to the saga — no separate
	// confirm overlay needed.
	if a.sagaProvide != nil {
		provide := a.sagaProvide
		preInput := a.sagaPreInput
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
		a.wizard.Clear()
		a.review.Clear()
		provideCmd := func() tea.Msg { provide(merged); return nil }
		if a.sagaCh != nil {
			return a, tea.Batch(provideCmd, readSagaCmd(a.sagaCh))
		}
		return a, provideCmd
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
	// Run op collected via the wizard overlay: invoke Run via tea.Exec
	// so the subprocess gets a real TTY (bubbletea releases stdin/stdout
	// and re-acquires on return). Running in-process here would leave
	// bubbletea in raw-mode owning stdin while the Run's subprocess
	// tries to stream to the terminal — broken for any op that execs
	// ssh, a pager, an editor, etc.
	if msg.op != nil && msg.op.Run != nil && len(msg.op.Steps) == 0 {
		op := msg.op
		input := msg.input
		// Clear the wizard overlay first — otherwise its 'Working…' busy
		// state persists forever because nothing else dismisses it on
		// the Run path (saga paths call Clear inside startSaga).
		a.wizard.Clear()
		cmd := &actionExec{ctx: a.ctx, missing: nil, input: input, op: op}
		return a, tea.Exec(cmd, func(err error) tea.Msg {
			trace.Trace(a.ctx, "tui wizard done run", "op", op.Name, "err", err)
			return actionDoneMsg{err: err}
		})
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

// handleSagaOverlayKey routes a keystroke while the saga overlay is
// active. The overlay's HandleKey first claims navigation keys (j/k,
// enter/space on a category header, arrows, e/c); only if it returns
// false do we fall through to the dismiss path (esc always dismisses;
// enter dismisses a completed flat saga or a completed row-cursor).
func (a *app) handleSagaOverlayKey(key string) (tea.Model, tea.Cmd) {
	if a.saga.HandleKey(key) {
		return a, nil
	}
	// Post-completion grace: swallow esc/enter for a short beat so
	// the user has time to actually read the outcome instead of
	// their "confirm" muscle memory tearing the overlay down. The
	// auto-dismiss tick fires at the same grace boundary and closes
	// the overlay on its own. While the saga is still running esc
	// is allowed (it aborts).
	if !a.saga.dismissable() {
		return a, nil
	}
	if key == "esc" || (a.saga.done && key == "enter") {
		a.saga.Clear()
		return a, nil
	}
	return a, nil // consume all keys while saga is active
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
		trace.Trace(a.ctx, "tui saga needs input", "step", ev.Step, "fields", len(ev.Needs.Fields))
		a.sagaProvide = ev.Provide
		a.sagaPreInput = ev.State
		a.sagaConfirmPrompt = ""
		input := registry.Input{}

		// Long reason + single boolean field → use the scrollable
		// review overlay (viewport + yes/no buttons) so the user
		// can actually read and scroll the content.
		if ev.Needs.Reason != "" && len(ev.Needs.Fields) == 1 &&
			ev.Needs.Fields[0].EffectiveKind() == registry.KindBool {
			a.review.SetSize(a.width, a.height)
			a.review.Show(ev.Needs.Reason, ev.Needs.Fields[0],
				sagaNeedOp(ev.Needs), nil, input)
			return a, drain()
		}

		return a, tea.Batch(
			a.wizard.ShowWithPreamble(a.ctx, nil, sagaNeedOp(ev.Needs), ev.Needs.Fields, input, ev.Needs.Reason),
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
		cmds := []tea.Cmd{a.saga.DismissAfter(), a.refresh(), a.subscribeBus(), toastCmd}
		// When the saga ran from the detail view, re-fetch the
		// currently-selected item so the pane reflects any state it
		// just mutated (e.g. deploy updates last-deploy + container
		// status). The list refresh() above only repopulates the
		// table; a.detailItem is populated via a separate Get.
		if a.mode == modeDetail {
			if cmd := a.fetchDetail(); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		return a, tea.Batch(cmds...)
	}
	return a, drain()
}

// handleConfirmKey routes a key into the active confirm overlay and
// dispatches Yes/No to either the saga-resume Provide callback (paused
// saga) or the startSaga path (fresh Confirm-gated op).
func (a *app) handleConfirmKey(key string) (tea.Model, tea.Cmd) {
	accepted, confirmed := a.confirm.HandleKey(key)
	// The overlay returns accepted=true for navigation keys (arrows/tab)
	// that change the Yes/No cursor but don't make a decision. We must
	// NOT treat those as a final answer, or pressing → to focus Yes
	// would immediately abort the saga (pendingProvide drained, confirmed
	// still false because the cursor was flipped but Enter not pressed).
	// confirm.active goes false only on a terminal key (y/n/esc/enter).
	decided := accepted && !a.confirm.Active()
	trace.Trace(a.ctx, "tui confirm key", "key", key, "accepted", accepted, "confirmed", confirmed, "decided", decided, "pendingProvide", a.pendingProvide != nil)
	if !decided {
		return a, nil
	}
	// Case 1: saga-resume confirm.
	if a.pendingProvide != nil {
		provide := a.pendingProvide
		merged := a.pendingMerged
		a.pendingProvide = nil
		a.pendingMerged = nil
		trace.Trace(a.ctx, "tui confirm routed", "path", "saga-resume", "confirmed", confirmed)
		if confirmed {
			provideCmd := func() tea.Msg { provide(merged); return nil }
			if a.sagaCh != nil {
				return a, tea.Batch(provideCmd, readSagaCmd(a.sagaCh))
			}
			return a, provideCmd
		}
		abortCmd := func() tea.Msg { provide(nil); return nil }
		if a.sagaCh != nil {
			return a, tea.Batch(abortCmd, readSagaCmd(a.sagaCh))
		}
		return a, abortCmd
	}
	// Case 2: fresh-saga confirm (Operation.Confirm). Guard: only fire
	// startSaga if the op actually has Steps or Run — protects against
	// a stray synthetic op slipping through.
	if confirmed && a.confirm.op != nil && (len(a.confirm.op.Steps) > 0 || a.confirm.op.Run != nil) {
		trace.Trace(a.ctx, "tui confirm routed", "path", "fresh-saga", "op", a.confirm.op.Name)
		return a, func() tea.Msg {
			return startSagaMsg{
				resource: a.confirm.resource,
				op:       a.confirm.op,
				input:    a.confirm.input,
			}
		}
	}
	trace.Trace(a.ctx, "tui confirm routed", "path", "dismiss", "confirmed", confirmed)
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

	// Minimized saga doesn't grab focus — the user is navigating the
	// underlying table while a small status pill sits in the corner.
	// Don't dim the background and don't count it as an "active overlay".
	sagaExpanded := a.saga.Expanded()
	overlayActive := a.showHelp || a.showPalette || sagaExpanded || a.confirm.Active() || a.wizard.Active() || a.review.Active()
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
	if sagaExpanded {
		overlays = append(overlays, centeredLayer(a.saga.Box(w, h, a.spin.View(), a.spinCategory.View()), w, h, 4))
	}
	if a.saga.Minimized() {
		overlays = append(overlays, topRightLayer(a.saga.Pill(), w, 4))
	}
	if a.confirm.Active() {
		overlays = append(overlays, centeredLayer(a.confirm.Box(w, h), w, h, 5))
	}
	if a.wizard.Active() {
		// Wizard is full-screen-ish; render it centered and large.
		overlays = append(overlays, centeredLayer(a.wizard.Box(w, h), w, h, 5))
	}
	if a.review.Active() {
		overlays = append(overlays, centeredLayer(a.review.Box(w, h), w, h, 6))
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

// topRightLayer pins content to the top-right corner of the screen,
// one row down from the very top so the banner ascii art isn't
// occluded. Used by the minimized saga pill.
func topRightLayer(content string, w, z int) *lipgloss.Layer {
	cw := lipgloss.Width(content)
	x := w - cw - 1
	if x < 0 {
		x = 0
	}
	return lipgloss.NewLayer(content).X(x).Y(0).Z(z)
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
// The digit shown in each hint MUST mirror the numeric-switch mapping in
// handleKey: resources 1..9 get "<1>".."<9>", the 10th gets "<0>". Keeping
// the two in lockstep is why the digit is computed here rather than just
// using the slice index.
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
		digit := i + 1
		if digit == 10 {
			digit = 0
		}
		key := theme.Key.Render(fmt.Sprintf("<%d>", digit))
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
		item := a.items[a.cursor]
		if a.detailItem != nil {
			item = a.detailItem
		}
		return detailFor(a.resource, item)
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
	now := time.Now()
	pk := primaryKeyField(a.resource)
	for i := start; i < end; i++ {
		rv := reflectIndirect(items[i])
		var pkVal string
		if pk != nil {
			pkVal = readField(rv, *pk)
		}
		row := make([]string, len(fields))
		for j, f := range fields {
			var val string
			if f.Async != nil && pkVal != "" {
				val = a.renderAsyncCell(pkVal, f)
			} else {
				val = readField(rv, f)
				if f.Table.Tick && val != "" {
					if t, err := time.Parse(time.RFC3339, val); err == nil {
						val = duration.Short(now.Sub(t))
					}
				}
			}
			row[j] = val
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
	// Ops that opted into HideFromStatusBar are omitted here but remain
	// available via the "?" help overlay and the command palette.
	var keys []string
	for _, b := range a.keyMap() {
		if b.Cat == "Navigation" {
			continue
		}
		if b.HideFromStatusBar {
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

type detailFetchedMsg struct{ item any }

// fetchDetail kicks off an async Get for the currently selected item
// so the detail view can show enriched data (statuses, endpoints, etc.).
func (a *app) fetchDetail() tea.Cmd {
	if a.resource == nil || a.resource.Store == nil || a.cursor >= len(a.items) {
		return nil
	}
	id := detailID(a.resource, a.items[a.cursor])
	if id == "" {
		return nil
	}
	store := a.resource.Store
	ctx := a.ctx
	return func() tea.Msg {
		item, err := store.Get(ctx, id)
		if err != nil {
			return detailFetchedMsg{item: nil}
		}
		return detailFetchedMsg{item: item}
	}
}
