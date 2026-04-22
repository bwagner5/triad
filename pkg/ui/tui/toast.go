package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/bwagner5/triad/pkg/ui/theme"
)

// toastLevel indicates color (green/red) for flash messages.
type toastLevel int

const (
	toastOK toastLevel = iota
	toastErr
)

// toast is a single transient top-of-screen message.
type toast struct {
	level toastLevel
	text  string
	until time.Time
}

func (t toast) view() string {
	if t.level == toastErr {
		return theme.ToastErr.Render(t.text)
	}
	return theme.ToastOK.Render(t.text)
}

// showToast enqueues a toast and schedules its auto-dismissal.
// Duration is a short, fixed window suitable for ephemeral feedback.
func (a *app) showToast(level toastLevel, text string) tea.Cmd {
	a.toast = &toast{level: level, text: text, until: time.Now().Add(3 * time.Second)}
	return tea.Tick(3*time.Second, func(_ time.Time) tea.Msg { return toastExpireMsg{} })
}

// toastExpireMsg clears any expired toast.
type toastExpireMsg struct{}
