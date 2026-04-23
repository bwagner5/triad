package runtime

import (
	"sync"
	"time"
)

// Scheduler decides when a resource should be polled in the TUI.
// Steady state is Slow. After a mutation saga completes on a resource,
// Bump switches that resource to Fast polling for FastWindow.
type Scheduler struct {
	Slow       time.Duration
	Fast       time.Duration
	FastWindow time.Duration
	Clock      Clock

	mu    sync.Mutex
	until map[string]time.Time // resource -> deadline for fast polling
}

func NewScheduler() *Scheduler {
	return &Scheduler{
		Slow:       10 * time.Second,
		Fast:       1 * time.Second,
		FastWindow: 10 * time.Second,
		Clock:      RealClock{},
		until:      map[string]time.Time{},
	}
}

// Interval returns the current poll interval for a resource.
func (s *Scheduler) Interval(resource string) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.until[resource]; ok && s.Clock.Now().Before(t) {
		return s.Fast
	}
	return s.Slow
}

// Bump switches the named resource to fast polling for FastWindow.
func (s *Scheduler) Bump(resource string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.until[resource] = s.Clock.Now().Add(s.FastWindow)
}
