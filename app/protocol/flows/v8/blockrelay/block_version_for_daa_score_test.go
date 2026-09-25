package blockrelay

import (
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/utils/constants"
)

// TestBlockVersionForDAAScoreIgnoresTheGlobal pins that the relay flow's version check for a block
// comes from that block's own DAA score only. An old-version block relayed after this node has seen
// (or built) a newer one must still be expected at its old version, not rejected - and its sender
// banned - for not matching the ratcheted process-global.
func TestBlockVersionForDAAScoreIgnoresTheGlobal(t *testing.T) {
	defer constants.ForceSetBlockVersion(1)
	powScores := []uint64{100, 200}

	constants.ForceSetBlockVersion(10)
	for _, test := range []struct {
		daaScore uint64
		expected uint16
	}{{0, 1}, {99, 1}, {100, 2}, {199, 2}, {200, 3}, {1 << 40, 3}} {
		if got := blockVersionForDAAScore(powScores, test.daaScore); got != test.expected {
			t.Errorf("DAA score %d with the global at 10: expected version %d, got %d",
				test.daaScore, test.expected, got)
		}
	}
}
