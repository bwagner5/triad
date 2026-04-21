package tui

import (
	"sort"

	tea "charm.land/bubbletea/v2"
	"github.com/bwagner5/go-cli-template/pkg/registry"
)

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

	// ---- Resource (contextual: from current resource's sagas + actions) ----
	if a.resource != nil {
		sagaNames := make([]string, 0, len(a.resource.Sagas))
		for name := range a.resource.Sagas {
			sagaNames = append(sagaNames, name)
		}
		sort.Strings(sagaNames)
		for _, name := range sagaNames {
			s := a.resource.Sagas[name]
			if s.Key == "" {
				continue
			}
			bs = append(bs, binding{
				Key: s.Key, Label: s.Name, Cat: "Resource",
				Run: func(a *app) (tea.Model, tea.Cmd) {
					var input registry.Input
					if s.Name == "delete" {
						input = a.selectedInput()
					}
					return a, a.launchSaga(a.resource, &s, input)
				},
			})
		}
		actNames := make([]string, 0, len(a.resource.Actions))
		for name := range a.resource.Actions {
			actNames = append(actNames, name)
		}
		sort.Strings(actNames)
		for _, name := range actNames {
			act := a.resource.Actions[name]
			if act.Key == "" {
				continue
			}
			bs = append(bs, binding{
				Key: act.Key, Label: act.Verb, Cat: "Resource",
				Run: func(a *app) (tea.Model, tea.Cmd) {
					return a, a.launchAction(a.resource, &act, a.selectedInput())
				},
			})
		}
	}
	bs = append(bs, binding{Key: "r", Label: "refresh", Cat: "Resource", Run: func(a *app) (tea.Model, tea.Cmd) {
		return a, a.refresh()
	}})

	// ---- Global ----
	bs = append(bs,
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
