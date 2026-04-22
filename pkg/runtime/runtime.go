// Package runtime executes sagas and broadcasts step events to UI consumers.
package runtime

import (
	"context"
	"sync"
	"time"

	"github.com/bwagner5/triad/pkg/registry"
)

// Status of a single saga step.
type Status int

const (
	Pending Status = iota
	Running
	OK
	Failed
	Skipped
)

func (s Status) String() string {
	switch s {
	case Pending:
		return "pending"
	case Running:
		return "running"
	case OK:
		return "ok"
	case Failed:
		return "failed"
	case Skipped:
		return "skipped"
	}
	return "unknown"
}

// Event is emitted for every step transition, plus a final Done event.
type Event struct {
	Saga     string
	Resource string
	Step     string // empty for the final Done event
	Index    int    // step index; -1 for final
	Total    int
	Status   Status
	Err      error
	At       time.Time
	Done     bool // true on the final event
}

// Run executes an operation's steps synchronously and streams events over the returned channel.
// The channel is closed when the operation is complete.
func Run(ctx context.Context, res registry.Resource, op registry.Operation, in registry.Input) <-chan Event {
	ch := make(chan Event, len(op.Steps)+2)
	go func() {
		defer close(ch)
		st := &registry.State{Input: in, Data: map[string]any{}}
		total := len(op.Steps)
		var runErr error
		var lastIdx int
		for i, step := range op.Steps {
			lastIdx = i
			if step.Skip != nil && step.Skip(st) {
				ch <- Event{Saga: op.Name, Resource: res.Name, Step: step.Label, Index: i, Total: total, Status: Skipped, At: time.Now()}
				continue
			}
			ch <- Event{Saga: op.Name, Resource: res.Name, Step: step.Label, Index: i, Total: total, Status: Running, At: time.Now()}
			if err := step.Do(ctx, st); err != nil {
				ch <- Event{Saga: op.Name, Resource: res.Name, Step: step.Label, Index: i, Total: total, Status: Failed, Err: err, At: time.Now()}
				runErr = err
				for j := i - 1; j >= 0; j-- {
					if op.Steps[j].Undo != nil {
						_ = op.Steps[j].Undo(ctx, st)
					}
				}
				break
			}
			ch <- Event{Saga: op.Name, Resource: res.Name, Step: step.Label, Index: i, Total: total, Status: OK, At: time.Now()}
		}
		final := Event{Saga: op.Name, Resource: res.Name, Index: -1, Total: total, Status: OK, At: time.Now(), Done: true}
		if runErr != nil {
			final.Status = Failed
			final.Err = runErr
			final.Index = lastIdx
		}
		defaultBus.Publish(final)
		ch <- final
	}()
	return ch
}

// ---- Event bus (for TUI refresh scheduler + cross-component signaling) ----

type Bus struct {
	mu   sync.RWMutex
	subs []chan Event
}

var defaultBus = &Bus{}

// DefaultBus returns the process-wide event bus. Any successful saga
// publishes its Done event here so the refresh scheduler can bump polling.
func DefaultBus() *Bus { return defaultBus }

// Subscribe returns a channel that receives future events. Caller must Unsubscribe.
func (b *Bus) Subscribe() chan Event {
	ch := make(chan Event, 16)
	b.mu.Lock()
	b.subs = append(b.subs, ch)
	b.mu.Unlock()
	return ch
}

func (b *Bus) Unsubscribe(ch chan Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, s := range b.subs {
		if s == ch {
			b.subs = append(b.subs[:i], b.subs[i+1:]...)
			close(s)
			return
		}
	}
}

func (b *Bus) Publish(e Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subs {
		select {
		case ch <- e:
		default: // drop if slow consumer
		}
	}
}
