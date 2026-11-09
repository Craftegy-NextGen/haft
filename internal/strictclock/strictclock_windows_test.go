package strictclock

import (
	"sync"
	"testing"
	"time"
)

// TestStrictAdvancesBetweenImmediateReadsOfTheSystemClock is the Windows
// regression for zse-6n75w: time.Now moves only at the system timer tick
// (1 of 200000 immediate read pairs advanced when measured), so a bare read
// pair is equal while the strict clock moves every time.
func TestStrictAdvancesBetweenImmediateReadsOfTheSystemClock(t *testing.T) {
	clock := Strict(time.Now)
	previous := clock()
	for index := 0; index < 2000; index++ {
		next := clock()
		if !next.Round(0).After(previous.Round(0)) {
			t.Fatalf("reading %d = %s did not advance after %s", index, next, previous)
		}
		previous = next
	}
}

// TestStrictServesTheDeclarationPollWithTheSystemClock models
// nextObservedTime: read once, then read again and require a later time, four
// times in a row, many times over.
func TestStrictServesTheDeclarationPollWithTheSystemClock(t *testing.T) {
	for run := 0; run < 200; run++ {
		clock := Strict(time.Now)
		previous := clock().UTC().Round(0)
		for step := 0; step < 4; step++ {
			observed := clock().UTC().Round(0)
			if !observed.After(previous) {
				t.Fatalf("run %d step %d: %s is not after %s", run, step, observed, previous)
			}
			previous = observed
		}
	}
}

func TestStrictReturnsRealReadingsOfTheWrappedClock(t *testing.T) {
	before := time.Now()
	clock := Strict(time.Now)
	first := clock()
	second := clock()
	after := time.Now()
	for _, reading := range []time.Time{first, second} {
		if reading.Before(before) || reading.After(after) {
			t.Fatalf("reading %s is outside the observation window %s .. %s", reading, before, after)
		}
	}
}

func TestStrictGivesUpOnAFrozenClockAfterTheWaitLimit(t *testing.T) {
	frozen := time.Date(2026, 11, 9, 1, 2, 3, 4, time.UTC)
	clock := Strict(func() time.Time { return frozen })
	first := clock()
	started := time.Now()
	second := clock()
	elapsed := time.Since(started)
	if !first.Equal(frozen) || !second.Equal(frozen) {
		t.Fatalf("frozen readings = %s and %s, want both %s", first, second, frozen)
	}
	if elapsed < waitLimit {
		t.Fatalf("a frozen clock returned after %s, want a wait of at least %s", elapsed, waitLimit)
	}
	if elapsed > 20*waitLimit {
		t.Fatalf("a frozen clock waited %s, want about %s", elapsed, waitLimit)
	}
}

func TestStrictSerializesConcurrentReadersIntoDistinctIncreasingTimes(t *testing.T) {
	clock := Strict(time.Now)
	const readers, readings = 8, 200
	var mu sync.Mutex
	seen := make(map[int64]bool, readers*readings)
	var wg sync.WaitGroup
	for reader := 0; reader < readers; reader++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := 0; index < readings; index++ {
				wall := clock().Round(0).UnixNano()
				mu.Lock()
				duplicate := seen[wall]
				seen[wall] = true
				mu.Unlock()
				if duplicate {
					t.Errorf("two readers observed the same instant %d", wall)
					return
				}
			}
		}()
	}
	wg.Wait()
}
