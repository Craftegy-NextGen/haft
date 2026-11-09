package strictclock

import (
	"testing"
	"time"
)

// TestStrictIsIdentityOnAStrictlyIncreasingClock is the identity proof that
// declcheck names for the Unix call sites (ADR-091): for a clock that already
// advances at every reading, the wrapped clock reads exactly the same times on
// every OS, so wrapping the default clock changes no Unix behaviour.
func TestStrictIsIdentityOnAStrictlyIncreasingClock(t *testing.T) {
	base := time.Date(2026, 11, 9, 1, 2, 3, 0, time.UTC)
	calls := 0
	clock := func() time.Time {
		calls++
		return base.Add(time.Duration(calls) * time.Nanosecond)
	}
	strict := Strict(clock)
	for index := 1; index <= 1000; index++ {
		got := strict()
		want := base.Add(time.Duration(index) * time.Nanosecond)
		if !got.Equal(want) {
			t.Fatalf("reading %d = %s, want %s", index, got, want)
		}
	}
	if calls != 1000 {
		t.Fatalf("wrapped clock read %d times, want exactly 1000", calls)
	}
}

func TestStrictKeepsANilClockNil(t *testing.T) {
	if Strict(nil) != nil {
		t.Fatal("Strict(nil) must stay nil on every OS")
	}
}
