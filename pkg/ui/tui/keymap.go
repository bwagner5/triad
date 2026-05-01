package tui

import (
	"sort"

	tea "charm.land/bubbletea/v2"
	"github.com/bwagner5/triad/pkg/registry"
	"github.com/bwagner5/triad/pkg/trace"
)

// needsSelection returns true when an operation acts on an existing resource
// (i.e. at least one field uses Suggest to pick from existing items).
func needsSelection(res registry.Resource, op registry.Operation) bool {
	// An operation "needs selection" when it acts on an existing resource
	// — i.e., it has a Suggest field whose flag matches the resource's
	// primary key (first table field). Operations like "create" have
	// Suggest fields for picking blueprints/bundles but don't select an
	// existing resource, so they should NOT pre-populate from the table.
	pk := ""
	for _, f := range res.Fields {
		if f.Table.Header != "" {
			pk = f.Flag
			break
		}
	}
	for _, f := range op.Fields {
		if f.Flag == pk && f.Suggest != nil {
			return true
		}
	}
	return false
}

// binding pairs a keystroke with a description and a handler. Categories
// drive the help overlay layout.
type binding struct {
	Key   string // keystroke as produced by tea.KeyPressMsg.String()
	Label string // short description shown in help + status bar
	Cat   string // one of: "Navigation", "Resource", "Global"
	Run   func(a *app) (tea.Model, tea.Cmd)
}

// keyMap returns the full set of key bindings for the current screen.
// Order within each category is the rendering order.
func (a *app) keyMap() []binding {
	var bs []binding

	// ---- Navigation ----
	bs = append(bs,
		binding{Key: "j", Label: "down", Cat: "Navigation", Run: func(a *app) (tea.Model, tea.Cmd) {
			if a.cursor < len(a.items)-1 {
				a.cursor++
			}
			return a, nil
		}},
		binding{Key: "k", Label: "up", Cat: "Navigation", Run: func(a *app) (tea.Model, tea.Cmd) {
			if a.cursor > 0 {
				a.cursor--
			}
			return a, nil
		}},
		binding{Key: "enter", Label: "detail", Cat: "Navigation", Run: func(a *app) (tea.Model, tea.Cmd) {
			if a.mode == modeList && len(a.items) > 0 {
				a.mode = modeDetail
				a.detailItem = nil // show list item immediately, enrich async
				return a, a.fetchDetail()
			}
			return a, nil
		}},
		binding{Key: "esc", Label: "back", Cat: "Navigation", Run: func(a *app) (tea.Model, tea.Cmd) {
			a.mode = modeList
			a.detailItem = nil
			return a, nil
		}},
		binding{Key: "backspace", Label: "back", Cat: "Navigation", Run: func(a *app) (tea.Model, tea.Cmd) {
			a.mode = modeList
			return a, nil
		}},
	)

	// ---- Resource (contextual: from current resource's operations) ----
	if a.resource != nil {
		opNames := make([]string, 0, len(a.resource.Operations))
		for name := range a.resource.Operations {
			opNames = append(opNames, name)
		}
		sort.Strings(opNames)
		for _, name := range opNames {
			op := a.resource.Operations[name]
			if op.Key == "" {
				continue
			}
			// Skip operations disabled for the currently selected item.
			if op.Enabled != nil && len(a.items) > 0 && a.cursor < len(a.items) {
				if !op.Enabled(a.items[a.cursor]) {
					continue
				}
			}
			bs = append(bs, binding{
				Key: op.Key, Label: op.Name, Cat: "Resource",
				Run: func(a *app) (tea.Model, tea.Cmd) {
					var input registry.Input
					if needsSelection(*a.resource, op) {
						input = a.selectedInput()
					}
					return a, a.launchOp(a.resource, &op, input)
				},
			})
		}
	}
	bs = append(bs, binding{Key: "r", Label: "refresh", Cat: "Resource", Run: func(a *app) (tea.Model, tea.Cmd) {
		return a, a.refresh()
	}})

	// ---- Global ----
	// Caller-supplied cross-cutting ops (region switch, etc.) come before
	// the built-in global keys so they're surfaced prominently.
	for i := range a.opts.GlobalOps {
		op := a.opts.GlobalOps[i] // capture by copy
		if op.Key == "" {
			continue
		}
		bs = append(bs, binding{
			Key: op.Key, Label: op.Name, Cat: "Global",
			Run: func(a *app) (tea.Model, tea.Cmd) {
				return a, a.launchOp(nil, &op, nil)
			},
		})
	}
	bs = append(bs,
		binding{Key: "/", Label: "filter", Cat: "Global", Run: func(a *app) (tea.Model, tea.Cmd) {
			a.filtering = true
			a.filterTI.SetValue(a.filterText)
			return a, a.filterTI.Focus()
		}},
		binding{Key: ":", Label: "palette", Cat: "Global", Run: func(a *app) (tea.Model, tea.Cmd) {
			a.showPalette = true
			a.palette.Focus()
			return a, nil
		}},
		binding{Key: "?", Label: "help", Cat: "Global", Run: func(a *app) (tea.Model, tea.Cmd) {
			a.showHelp = true
			return a, nil
		}},
		binding{Key: "q", Label: "quit", Cat: "Global", Run: func(a *app) (tea.Model, tea.Cmd) {
			return a, tea.Quit
		}},
	)
	return bs
}

// dispatch invokes the first binding whose Key matches. Returns ok=false when
// no binding handles the key.
func (a *app) dispatch(key string) (tea.Model, tea.Cmd, bool) {
	for _, b := range a.keyMap() {
		if b.Key == key {
			trace.Log("tui.dispatch", "key", key, "label", b.Label, "cat", b.Cat)
			m, cmd := b.Run(a)
			return m, cmd, true
		}
	}
	trace.Log("tui.dispatch.miss", "key", key)
	return a, nil, false
}
