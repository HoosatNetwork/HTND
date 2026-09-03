package consensus

import (
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/domain/consensus/utils/constants"
)

// TestExpectedDAAWindowDurationFollowsActiveBlockVersion pins that the nearly-synced threshold is
// derived from the block version that is active at call time, not from whatever it happened to be
// when the consensus was constructed.
//
// This used to be precomputed in the factory. constants.GetBlockVersion() is a process-global atomic
// that starts at 1 and is only raised at runtime as blocks arrive, so a consensus built at startup
// froze the v1 window (~44 min) while a staging consensus built mid-IBD froze the v6 one (~8.8 min).
// Transaction relay is disabled whenever a node is not nearly synced, so that discrepancy made
// otherwise-identical nodes stop relaying at different lag thresholds.
func TestExpectedDAAWindowDurationFollowsActiveBlockVersion(t *testing.T) {
	original := constants.GetBlockVersion()
	defer constants.ForceSetBlockVersion(uint(original))

	s := &consensus{
		targetTimePerBlock:             []time.Duration{1 * time.Second, 1 * time.Second, 200 * time.Millisecond},
		difficultyAdjustmentWindowSize: []int{2641, 2641, 2640},
	}

	constants.ForceSetBlockVersion(1)
	if got, want := s.expectedDAAWindowDurationInMilliseconds(), int64(2641000); got != want {
		t.Fatalf("block version 1: got %d ms, want %d ms", got, want)
	}

	// The same consensus object, after the version ratchets up, must report the new version's window.
	constants.ForceSetBlockVersion(3)
	if got, want := s.expectedDAAWindowDurationInMilliseconds(), int64(528000); got != want {
		t.Fatalf("block version 3: got %d ms, want %d ms", got, want)
	}
}

// TestExpectedDAAWindowDurationClampsOutOfRangeVersion pins that a block version beyond the
// configured tables clamps to the last entry instead of panicking. SetBlockVersion is a one-way
// ratchet driven by relayed blocks, so an out-of-range version is reachable from the network and
// must not be able to take the node down.
func TestExpectedDAAWindowDurationClampsOutOfRangeVersion(t *testing.T) {
	original := constants.GetBlockVersion()
	defer constants.ForceSetBlockVersion(uint(original))

	s := &consensus{
		targetTimePerBlock:             []time.Duration{1 * time.Second, 200 * time.Millisecond},
		difficultyAdjustmentWindowSize: []int{2641, 2640},
	}

	constants.ForceSetBlockVersion(99)
	if got, want := s.expectedDAAWindowDurationInMilliseconds(), int64(528000); got != want {
		t.Fatalf("out-of-range version: got %d ms, want the last table entry %d ms", got, want)
	}
}
