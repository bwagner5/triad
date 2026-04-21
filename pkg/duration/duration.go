// Package duration formats time.Durations using the largest whole unit
// (e.g. 10s, 1m, 2m, 1h). It's used in the TUI for compact refresh counters.
package duration

import (
	"fmt"
	"time"
)

// Short renders d using the largest whole unit: s, m, h, or d.
// Sub-second values become "0s"; negative values become "0s".
func Short(d time.Duration) string {
	if d < time.Second {
		return "0s"
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
