package runtime_test

import (
	"testing"
	"time"

	"github.com/bwagner5/triad/pkg/runtime"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time      { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func TestSchedulerBumpAndDecay(t *testing.T) {
	fc := &fakeClock{t: time.Unix(0, 0)}
	s := runtime.NewScheduler()
	s.Clock = fc

	if got := s.Interval("r"); got != s.Slow {
		t.Errorf("steady-state Interval = %v, want %v", got, s.Slow)
	}
	s.Bump("r")
	if got := s.Interval("r"); got != s.Fast {
		t.Errorf("post-bump Interval = %v, want %v", got, s.Fast)
	}
	// Still inside the fast window.
	fc.advance(s.FastWindow - time.Millisecond)
	if got := s.Interval("r"); got != s.Fast {
		t.Errorf("inside fast window Interval = %v, want %v", got, s.Fast)
	}
	// Past the window → back to slow.
	fc.advance(2 * time.Millisecond)
	if got := s.Interval("r"); got != s.Slow {
		t.Errorf("after fast window Interval = %v, want %v", got, s.Slow)
	}
}

func TestSchedulerBumpIsResourceScoped(t *testing.T) {
	fc := &fakeClock{t: time.Unix(0, 0)}
	s := runtime.NewScheduler()
	s.Clock = fc

	s.Bump("a")
	if got := s.Interval("a"); got != s.Fast {
		t.Errorf("a should be fast: %v", got)
	}
	if got := s.Interval("b"); got != s.Slow {
		t.Errorf("b should be slow (not bumped): %v", got)
	}
}
