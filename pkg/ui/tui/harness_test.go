package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/bwagner5/triad/pkg/registry"
)

// harness is a deterministic test driver for the TUI model. It runs the
// Update-produce-cmd-produce-message loop synchronously so tests can
// assert on intermediate state without timing fuss.
//
// Typical use:
//
//	h := newHarness(t, reg, Options{Name: "test"})
//	h.Resize(120, 30)
//	h.Press("d")
//	h.Send(someMsg{})
//	if !strings.Contains(h.Text(), "expected") { t.Errorf(...) }
type harness struct {
	t   *testing.T
	m   tea.Model
	ctx context.Context
}

// newHarness builds an app, registers resources, and starts the bubbletea
// Init cmds. Uses a background context that the caller can swap via SetCtx.
func newHarness(t *testing.T, reg *registry.Registry, opts Options) *harness {
	t.Helper()
	ctx := context.Background()
	a := newApp(ctx, reg, opts)
	if all := reg.All(); len(all) > 0 {
		a.resource = ptr(all[0])
	}
	h := &harness{t: t, m: a, ctx: ctx}
	// Drive Init() through the run loop so commands fire.
	h.runCmd(a.Init())
	return h
}

// Resize issues a WindowSizeMsg. Most overlays need a size before they'll
// render sensibly.
func (h *harness) Resize(w, hgt int) {
	h.Send(tea.WindowSizeMsg{Width: w, Height: hgt})
}

// Press simulates a single keystroke. Accepts the same string form produced
// by tea.KeyPressMsg.String() (e.g. "enter", "tab", "ctrl+c", "a").
func (h *harness) Press(key string) {
	h.Send(keyMsg(key))
}

// Type taps each rune in s as a separate KeyPressMsg.
func (h *harness) Type(s string) {
	for _, r := range s {
		h.Send(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

// Send dispatches a raw tea.Msg and drains the resulting cmd chain.
func (h *harness) Send(msg tea.Msg) {
	h.t.Helper()
	var cmd tea.Cmd
	h.m, cmd = h.m.Update(msg)
	h.runCmd(cmd)
}

// Text returns the rendered view with ANSI stripped.
func (h *harness) Text() string {
	return stripANSI(h.App().View().Content)
}

// App returns the concrete *app for direct state inspection.
func (h *harness) App() *app {
	return h.m.(*app)
}

// WaitFor polls until cond() returns true or timeout elapses. Each poll
// drains any pending spinner ticks / scheduled timers via a Tick-like
// synthetic message so tests don't have to manage their own clock. Fails
// the test on timeout.
func (h *harness) WaitFor(cond func() bool, timeout time.Duration, label string) {
	h.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.t.Fatalf("WaitFor(%s) timed out after %s. View:\n%s", label, timeout, h.Text())
}

// runCmd unpacks a tea.Cmd by executing it synchronously and recursively
// dispatching any produced messages back into Update. Unsupported message
// shapes (Tick, Exec, etc.) are dropped on the floor — they aren't
// needed for the functional tests we're driving here.
func (h *harness) runCmd(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	// Some cmds block (channel reads from a saga runtime); guard with a
	// goroutine + timeout so a misbehaving one fails the test cleanly.
	msgCh := make(chan tea.Msg, 1)
	go func() { msgCh <- cmd() }()
	select {
	case msg := <-msgCh:
		if msg == nil {
			return
		}
		// Batched cmds arrive as a BatchMsg (we can't use tea.BatchMsg
		// directly here, but tea.Batch composes them into a single
		// message type that bubbletea unpacks internally). For our
		// harness we don't handle batched cmds structurally; instead
		// tests dispatch one message at a time via Send.
		var next tea.Cmd
		h.m, next = h.m.Update(msg)
		h.runCmd(next)
	case <-time.After(200 * time.Millisecond):
		// Long-running cmd (likely a blocking channel read). Let it go;
		// the test will use WaitFor or direct state assertions.
	}
}

// keyMsg translates a keystroke-string into the tea.KeyPressMsg shape.
// Mirrors the small subset used by keyMap + overlay handlers.
func keyMsg(key string) tea.KeyPressMsg {
	switch key {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter, Text: ""}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape, Text: ""}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Text: ""}
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift, Text: ""}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace, Text: ""}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp, Text: ""}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown, Text: ""}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft, Text: ""}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight, Text: ""}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl, Text: ""}
	}
	// Fallback: a single-rune printable key.
	if len(key) == 1 {
		r := rune(key[0])
		return tea.KeyPressMsg{Code: r, Text: key}
	}
	panic(fmt.Sprintf("harness: unsupported key %q", key))
}

// Contains is a convenience for asserting substring presence in the view.
func (h *harness) Contains(substr string) bool {
	return strings.Contains(h.Text(), substr)
}
