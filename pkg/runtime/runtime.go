// Package runtime executes sagas and broadcasts step events to UI consumers.
package runtime

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/bwagner5/triad/pkg/registry"
	"github.com/bwagner5/triad/pkg/trace"
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
	// Output is an optional summary shown after saga completion.
	// Populated by the last step to surface results (e.g. URLs).
	Output string

	// ---- NeedsInput payload ----
	// Needs is populated when Status == NeedsInput. The consumer renders a
	// wizard over these fields, then calls Provide exactly once with the
	// collected answers (or a nil map to abort the saga).
	Needs   *registry.NeedInput
	Provide func(answers registry.Input)
	// State is a snapshot of the saga's Input at the moment it paused.
	// Consumers can merge it with wizard answers to render a confirm
	// summary that shows everything the step is about to run with, not
	// just the newly-collected fields.
	State registry.Input
}

// Run executes an operation's steps synchronously and streams events over the returned channel.
// The channel is closed when the operation is complete.
// If bus is non-nil, the final Done event is also published to it (for cross-component signaling).
func Run(ctx context.Context, bus *Bus, res registry.Resource, op registry.Operation, in registry.Input) <-chan Event {
	// Buffer: 3x steps covers Running+OK/Failed/Skipped plus NeedsInput
	// and Done headroom, so the runtime goroutine never blocks on send
	// even if the consumer drains slowly (per-render in a TUI).
	ch := make(chan Event, len(op.Steps)*3+4)
	go func() {
		defer close(ch)
		st := &registry.State{Input: in, Data: map[string]any{}}
		total := len(op.Steps)

		// Stamp saga-wide attrs on the logger attached to ctx. Every
		// downstream step Do function and any log emitted within it
		// inherits resource/saga/op without repeating them per call.
		sagaCtx := trace.WithAttrs(ctx,
			slog.String("resource", res.Name),
			slog.String("saga", op.Name),
			slog.String("op", op.Name),
		)
		sagaLog := trace.FromContext(sagaCtx)
		sagaStart := time.Now()
		sagaLog.InfoContext(sagaCtx, "saga start", slog.Int("total_steps", total))

		var runErr error
		var lastIdx int
		var okCount, failedCount, skippedCount int
		// Track the final aggregate Output separately from per-step
		// Output. Each step may set st.Output to describe its own
		// result (e.g. "Role ARN: …"); we copy that into the OK
		// event, then reset st.Output so the next step starts fresh.
		// finalOutput accumulates the last non-empty step output for
		// the Done event — matching the legacy behavior where a
		// saga's final Output was whatever the last step left in
		// state.
		var finalOutput string
		for i, step := range op.Steps {
			lastIdx = i

			// Per-step attrs on top of saga-wide attrs.
			stepCtx := trace.WithAttrs(sagaCtx,
				slog.String("step", step.Label),
				slog.Int("step_index", i),
				slog.Int("total_steps", total),
			)
			stepLog := trace.FromContext(stepCtx)

			if step.Skip != nil && step.Skip(st) {
				stepLog.InfoContext(stepCtx, "step skipped")
				skippedCount++
				ch <- Event{Saga: op.Name, Resource: res.Name, Step: step.Label, Index: i, Total: total, Status: Skipped, At: time.Now()}
				continue
			}
			st.Output = ""
			stepStart := time.Now()
			stepLog.InfoContext(stepCtx, "step start")
			// A step may return registry.NeedInput; the runtime pauses,
			// asks the consumer for the listed fields, and retries the
			// same step. Runs at most 8 times per step to prevent loops.
			var stepErr error
			for attempt := 0; attempt < 8; attempt++ {
				ch <- Event{Saga: op.Name, Resource: res.Name, Step: step.Label, Index: i, Total: total, Status: Running, At: time.Now()}
				stepErr = step.Do(stepCtx, st)
				var need *registry.NeedInput
				if !errors.As(stepErr, &need) {
					break
				}
				if !solicitInput(stepCtx, ch, op, res, step, i, total, st, need) {
					// consumer declined; treat as step failure
					stepErr = errors.New("input required but not provided")
					break
				}
				// retry with merged input
			}
			if stepErr != nil {
				stepLog.ErrorContext(stepCtx, "step failed",
					slog.Any("err", stepErr),
					slog.Duration("elapsed", time.Since(stepStart)))
				failedCount++
				ch <- Event{Saga: op.Name, Resource: res.Name, Step: step.Label, Index: i, Total: total, Status: Failed, Err: stepErr, At: time.Now()}
				runErr = stepErr
				for j := i - 1; j >= 0; j-- {
					if op.Steps[j].Undo != nil {
						_ = op.Steps[j].Undo(stepCtx, st)
					}
				}
				break
			}
			stepOutput := st.Output
			if stepOutput != "" {
				finalOutput = stepOutput
			}
			stepLog.InfoContext(stepCtx, "step ok",
				slog.Duration("elapsed", time.Since(stepStart)))
			okCount++
			ch <- Event{Saga: op.Name, Resource: res.Name, Step: step.Label, Index: i, Total: total, Status: OK, Output: stepOutput, At: time.Now()}
		}
		final := Event{Saga: op.Name, Resource: res.Name, Index: -1, Total: total, Status: OK, At: time.Now(), Done: true, Output: finalOutput}
		if runErr != nil {
			final.Status = Failed
			final.Err = runErr
			final.Index = lastIdx
			sagaLog.ErrorContext(sagaCtx, "saga failed",
				slog.Any("err", runErr),
				slog.Duration("elapsed", time.Since(sagaStart)),
				slog.Int("ok_count", okCount),
				slog.Int("failed_count", failedCount),
				slog.Int("skipped_count", skippedCount))
		} else {
			sagaLog.InfoContext(sagaCtx, "saga ok",
				slog.Duration("elapsed", time.Since(sagaStart)),
				slog.Int("ok_count", okCount),
				slog.Int("skipped_count", skippedCount))
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
	// Snapshot state so the consumer can render a confirm summary.
	snapshot := registry.Input{}
	for k, v := range st.Input {
		snapshot[k] = v
	}
	ch <- Event{
		Saga: op.Name, Resource: res.Name, Step: step.Label,
		Index: i, Total: total, Status: NeedsInput, At: time.Now(),
		Needs: need, Provide: provide, State: snapshot,
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
