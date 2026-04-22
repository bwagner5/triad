package tui

import (
	"sort"

	tea "charm.land/bubbletea/v2"
	"github.com/bwagner5/go-cli-template/pkg/registry"
)

// needsSelection returns true when an operation acts on an existing resource
// (i.e. at least one field uses Suggest to pick from existing items).
func needsSelection(op registry.Operation) bool {
	for _, f := range op.Fields {
		if f.Suggest != nil {
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
			}
			return a, nil
		}},
		binding{Key: "esc", Label: "back", Cat: "Navigation", Run: func(a *app) (tea.Model, tea.Cmd) {
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
			bs = append(bs, binding{
				Key: op.Key, Label: op.Name, Cat: "Resource",
				Run: func(a *app) (tea.Model, tea.Cmd) {
					var input registry.Input
					if needsSelection(op) {
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
			m, cmd := b.Run(a)
			return m, cmd, true
		}
	}
	return a, nil, false
}
