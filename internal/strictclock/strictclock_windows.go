package strictclock

import (
	"sync"
	"time"
)

const (
	// waitLimit bounds how long one reading waits for the wrapped clock to
	// move. It is far above the largest system timer tick (15.6 ms).
	waitLimit = 100 * time.Millisecond
	// pause is the sleep between two reads of a clock that has not moved.
	pause = 50 * time.Microsecond
)

// Strict returns a clock whose every reading is strictly later (as a wall
// instant, ignoring the monotonic reading) than the previous reading of the
// returned clock. When the wrapped clock has not advanced it is read again
// after a short sleep until it has, for at most waitLimit; then the latest
// reading is returned even if it did not advance. The returned clock is safe
// for concurrent use: readings are serialized, so concurrent callers also
// observe strictly increasing times. A nil clock returns nil.
func Strict(clock func() time.Time) func() time.Time {
	if clock == nil {
		return nil
	}
	var mu sync.Mutex
	var last time.Time
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		giveUp := time.Now().Add(waitLimit)
		observed := clock()
		for !observed.Round(0).After(last) && time.Now().Before(giveUp) {
			time.Sleep(pause)
			observed = clock()
		}
		if wall := observed.Round(0); wall.After(last) {
			last = wall
		}
		return observed
	}
}
