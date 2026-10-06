package constants

import (
	"math/rand"
	"sync"
	"testing"
)

// TestSetBlockVersionIsMonotonicUnderConcurrency guards the one-way-ratchet invariant every reader of
// GetBlockVersion depends on (see the "block-version global" note in the repo's CLAUDE.md):
// SetBlockVersion must never let the stored version decrease, including under concurrent calls with
// different proposed values - exactly what happens when several peers' own relay goroutines each
// process a different block's own version at the same time.
//
// This is a regression guard, not a reproduction: the load-then-store implementation this replaced
// was a genuine check-then-act race (two goroutines can both read the same current value before
// either writes, and if the lower proposed version's store lands after the higher one's, the stored
// version regresses), but a stress test of up to 2000 goroutines racing on a start barrier, repeated
// 20 times, never actually caught it regressing on this hardware - the window between the load and
// the store is apparently too narrow to hit reliably here. The fix (a CAS loop, so a version already
// advanced past a given proposal during the loop can never be overwritten by it) is correct
// regardless of whether the old code could be caught failing; this test pins the invariant going
// forward rather than proving the old bug fired.
func TestSetBlockVersionIsMonotonicUnderConcurrency(t *testing.T) {
	ForceSetBlockVersion(1)
	defer ForceSetBlockVersion(1)

	const goroutines = 200
	const itersPerGoroutine = 200
	const maxVersion = 50

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(seed int64) {
			defer wg.Done()
			r := rand.New(rand.NewSource(seed))
			for i := 0; i < itersPerGoroutine; i++ {
				SetBlockVersion(uint16(r.Intn(maxVersion) + 1))
			}
		}(int64(g))
	}
	wg.Wait()

	if got := GetBlockVersion(); got != maxVersion {
		t.Fatalf("expected the block version ratchet to settle at the highest value ever proposed "+
			"(%d), got %d - it regressed under concurrent SetBlockVersion calls", maxVersion, got)
	}
}

// TestSetBlockVersionNeverDecreasesSequentially pins the simple, single-threaded contract too: a
// lower value never overrides a higher one already set.
func TestSetBlockVersionNeverDecreasesSequentially(t *testing.T) {
	ForceSetBlockVersion(1)
	defer ForceSetBlockVersion(1)

	SetBlockVersion(5)
	if got := GetBlockVersion(); got != 5 {
		t.Fatalf("expected 5, got %d", got)
	}
	SetBlockVersion(3)
	if got := GetBlockVersion(); got != 5 {
		t.Fatalf("a lower value must not decrease the ratchet: expected 5, got %d", got)
	}
	SetBlockVersion(7)
	if got := GetBlockVersion(); got != 7 {
		t.Fatalf("expected 7, got %d", got)
	}
}
