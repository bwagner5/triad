package runtime

import "time"

// Clock abstracts time.Now so tests can control event timestamps and scheduler
// windows deterministically. Production code uses RealClock.
type Clock interface {
	Now() time.Time
}

// RealClock returns the wall-clock time.
type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }
