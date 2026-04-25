package runtime_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bwagner5/triad/pkg/registry"
	"github.com/bwagner5/triad/pkg/runtime"
)


func TestRunNeedsInputResumes(t *testing.T) {
	// Step "verify" fails first call with NeedInput; after the runtime
	// injects the answer, it succeeds.
	calls := 0
	verify := registry.Step{
		Label: "verify",
		Do: func(_ context.Context, s *registry.State) error {
			calls++
			if s.Input.Get("name") == "" {
				return &registry.NeedInput{
					Fields: []registry.Field{{Flag: "name", Required: true}},
					Reason: "app missing",
				}
			}
			return nil
		},
	}
	op := registry.Operation{Name: "deploy", Steps: []registry.Step{verify}}
	ch := runtime.Run(context.Background(), nil, registry.Resource{Name: "app"}, op, registry.Input{})

	sawNeed := false
	for e := range ch {
		if e.Status == runtime.NeedsInput {
			sawNeed = true
			if e.Needs == nil || e.Provide == nil {
				t.Fatalf("NeedsInput event missing payload: %+v", e)
			}
			// Supply the answer.
			e.Provide(registry.Input{"name": "foo"})
		}
	}
	if !sawNeed {
		t.Error("expected a NeedsInput event")
	}
	if calls != 2 {
		t.Errorf("verify called %d times, want 2 (first = NeedInput, second = ok)", calls)
	}
}

func TestRunNeedsInputAbort(t *testing.T) {
	verify := registry.Step{
		Label: "verify",
		Do: func(_ context.Context, _ *registry.State) error {
			return &registry.NeedInput{Fields: []registry.Field{{Flag: "x", Required: true}}}
		},
	}
	op := registry.Operation{Name: "deploy", Steps: []registry.Step{verify}}
	ch := runtime.Run(context.Background(), nil, registry.Resource{Name: "app"}, op, registry.Input{})

	for e := range ch {
		if e.Status == runtime.NeedsInput {
			e.Provide(nil) // abort
		}
	}
	// Nothing to assert structurally — the test proves the goroutine
	// terminates (no leak). If Provide(nil) deadlocked, go test would hang.
}
// drain reads all events from ch until closed.
func drain(ch <-chan runtime.Event) []runtime.Event {
	var out []runtime.Event
	for e := range ch {
		out = append(out, e)
	}
	return out
}

func step(label string, do func() error) registry.Step {
	return registry.Step{Label: label, Do: func(_ context.Context, _ *registry.State) error { return do() }}
}

func TestRunHappyPath(t *testing.T) {
	res := registry.Resource{Name: "thing"}
	op := registry.Operation{Name: "create", Steps: []registry.Step{
		step("a", func() error { return nil }),
		step("b", func() error { return nil }),
		step("c", func() error { return nil }),
	}}
	events := drain(runtime.Run(context.Background(), nil, res, op, registry.Input{}))

	// 3 Running + 3 OK + 1 Done = 7
	if len(events) != 7 {
		t.Fatalf("got %d events, want 7: %+v", len(events), events)
	}
	wantStatus := []runtime.Status{
		runtime.Running, runtime.OK,
		runtime.Running, runtime.OK,
		runtime.Running, runtime.OK,
		runtime.OK, // final Done
	}
	for i, e := range events {
		if e.Status != wantStatus[i] {
			t.Errorf("event[%d].Status=%v, want %v", i, e.Status, wantStatus[i])
		}
	}
	final := events[len(events)-1]
	if !final.Done || final.Status != runtime.OK || final.Err != nil {
		t.Errorf("final event wrong: %+v", final)
	}
}

func TestRunFailureRunsUndoInReverse(t *testing.T) {
	var undone []string
	boom := errors.New("boom")
	res := registry.Resource{Name: "thing"}
	op := registry.Operation{Name: "create", Steps: []registry.Step{
		{Label: "a",
			Do:   func(_ context.Context, _ *registry.State) error { return nil },
			Undo: func(_ context.Context, _ *registry.State) error { undone = append(undone, "a"); return nil }},
		{Label: "b",
			Do: func(_ context.Context, _ *registry.State) error { return boom },
			// No Undo here — should not be called since step failed before success.
		},
		{Label: "c",
			Do:   func(_ context.Context, _ *registry.State) error { t.Fatal("step c ran after failure"); return nil },
			Undo: func(_ context.Context, _ *registry.State) error { undone = append(undone, "c"); return nil }},
	}}
	events := drain(runtime.Run(context.Background(), nil, res, op, registry.Input{}))

	// Only step 'a' Undo runs (step b didn't complete; step c didn't start).
	if len(undone) != 1 || undone[0] != "a" {
		t.Errorf("undone = %v, want [a]", undone)
	}
	final := events[len(events)-1]
	if !final.Done || final.Status != runtime.Failed || !errors.Is(final.Err, boom) {
		t.Errorf("final event wrong: %+v", final)
	}
}

func TestRunSkip(t *testing.T) {
	res := registry.Resource{Name: "thing"}
	op := registry.Operation{Name: "create", Steps: []registry.Step{
		{Label: "skipme",
			Skip: func(_ *registry.State) bool { return true },
			Do:   func(_ context.Context, _ *registry.State) error { t.Fatal("Do ran on skipped step"); return nil }},
	}}
	events := drain(runtime.Run(context.Background(), nil, res, op, registry.Input{}))
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2 (skipped + done)", len(events))
	}
	if events[0].Status != runtime.Skipped {
		t.Errorf("events[0] status = %v, want Skipped", events[0].Status)
	}
	if !events[1].Done || events[1].Status != runtime.OK {
		t.Errorf("events[1] should be Done/OK: %+v", events[1])
	}
}

func TestRunContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	res := registry.Resource{Name: "thing"}
	op := registry.Operation{Name: "create", Steps: []registry.Step{
		{Label: "blocker", Do: func(ctx context.Context, _ *registry.State) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		}},
	}}
	ch := runtime.Run(ctx, nil, res, op, registry.Input{})
	<-started
	cancel()
	events := drain(ch)
	final := events[len(events)-1]
	if !final.Done || final.Status != runtime.Failed {
		t.Errorf("expected Failed Done after cancel, got %+v", final)
	}
}

// ---- §3 Bus ----

func TestBusMultipleSubscribersReceiveDone(t *testing.T) {
	bus := runtime.NewBus()
	s1, s2 := bus.Subscribe(), bus.Subscribe()
	res := registry.Resource{Name: "thing"}
	op := registry.Operation{Name: "create", Steps: []registry.Step{
		step("only", func() error { return nil }),
	}}
	drain(runtime.Run(context.Background(), bus, res, op, registry.Input{}))

	for i, s := range []chan runtime.Event{s1, s2} {
		select {
		case e := <-s:
			if !e.Done {
				t.Errorf("subscriber %d got non-Done event: %+v", i, e)
			}
		case <-time.After(time.Second):
			t.Errorf("subscriber %d received no event", i)
		}
	}
}

func TestBusSlowSubscriberDoesNotBlock(t *testing.T) {
	bus := runtime.NewBus()
	_ = bus.Subscribe() // never read

	// Publish past the internal buffer (16) — drops should kick in.
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			bus.Publish(runtime.Event{Saga: "s"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Publish blocked on slow subscriber")
	}
}

func TestBusUnsubscribeClosesChannel(t *testing.T) {
	bus := runtime.NewBus()
	s := bus.Subscribe()
	bus.Unsubscribe(s)
	if _, ok := <-s; ok {
		t.Error("channel not closed after Unsubscribe")
	}
	// Publishing after unsubscribe should not panic.
	bus.Publish(runtime.Event{Saga: "after"})
}
