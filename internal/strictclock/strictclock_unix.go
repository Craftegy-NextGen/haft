//go:build !windows

package strictclock

import "time"

// Strict returns clock unchanged: the Unix wall clock has nanosecond
// resolution, so no wrapper is needed.
func Strict(clock func() time.Time) func() time.Time {
	return clock
}
