// Package runtime executes sagas and broadcasts step events to UI consumers.
package runtime

import (
	"context"
	"errors"
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
	// NeedsInput is emitted when a step returned registry.NeedInput. The
	// saga is paused; the consumer must fill Event.Provide with the
	// collected fields (or call it with nil to abort).
	NeedsInput
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
	case NeedsInput:
		return "needs_input"
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

	// ---- NeedsInput payload ----
	// Needs is populated when Status == NeedsInput. The consumer renders a
	// wizard over these fields, then calls Provide exactly once with the
	// collected answers (or a nil map to abort the saga).
	Needs   *registry.NeedInput
	Provide func(answers registry.Input)
}

// Run executes an operation's steps synchronously and streams events over the returned channel.
// The channel is closed when the operation is complete.
// If bus is non-nil, the final Done event is also published to it (for cross-component signaling).
func Run(ctx context.Context, bus *Bus, res registry.Resource, op registry.Operation, in registry.Input) <-chan Event {
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
			// A step may return registry.NeedInput; the runtime pauses,
			// asks the consumer for the listed fields, and retries the
			// same step. Runs at most 8 times per step to prevent loops.
			var stepErr error
			for attempt := 0; attempt < 8; attempt++ {
				ch <- Event{Saga: op.Name, Resource: res.Name, Step: step.Label, Index: i, Total: total, Status: Running, At: time.Now()}
				stepErr = step.Do(ctx, st)
				var need *registry.NeedInput
				if !errors.As(stepErr, &need) {
					break
				}
				if !solicitInput(ctx, ch, op, res, step, i, total, st, need) {
					// consumer declined; treat as step failure
					stepErr = errors.New("input required but not provided")
					break
				}
				// retry with merged input
			}
			if stepErr != nil {
				ch <- Event{Saga: op.Name, Resource: res.Name, Step: step.Label, Index: i, Total: total, Status: Failed, Err: stepErr, At: time.Now()}
				runErr = stepErr
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
		if bus != nil {
			bus.Publish(final)
		}
		ch <- final
	}()
	return ch
}

// solicitInput emits a NeedsInput event with a Provide callback and waits
// for the consumer to reply. Returns true on success (answers merged into
// state), false if the consumer aborts.
func solicitInput(
	ctx context.Context, ch chan<- Event, op registry.Operation, res registry.Resource,
	step registry.Step, i, total int, st *registry.State, need *registry.NeedInput,
) bool {
	answer := make(chan registry.Input, 1)
	provide := func(in registry.Input) {
		select {
		case answer <- in:
		default:
		}
	}
	ch <- Event{
		Saga: op.Name, Resource: res.Name, Step: step.Label,
		Index: i, Total: total, Status: NeedsInput, At: time.Now(),
		Needs: need, Provide: provide,
	}
	select {
	case <-ctx.Done():
		return false
	case in := <-answer:
		if in == nil {
			return false
		}
		for k, v := range in {
			st.Input[k] = v
		}
		return true
	}
}

// ---- Event bus (for TUI refresh scheduler + cross-component signaling) ----

type Bus struct {
	mu   sync.RWMutex
	subs []chan Event
}

// NewBus creates an empty event bus.
func NewBus() *Bus { return &Bus{} }

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
