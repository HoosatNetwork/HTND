package consensus_test

import (
	"math"
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus"
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/testutils"
)

// TestDAABlockWindowFollowsTheBlocksOwnVersion pins that the DAA window a node reports for a block - served to IBD
// peers as trusted data - has the window size of that block's own version. It was sized by the process-global block
// version, so servers that had restarted and servers that had not handed syncing peers different windows for the
// same pruning point anticone block.
func TestDAABlockWindowFollowsTheBlocksOwnVersion(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		defer constants.ForceSetBlockVersion(1)

		// Every block is version 1, whose window holds 10 blocks; later versions have windows longer than the chain.
		consensusConfig.POWScores = []uint64{math.MaxUint64}
		versions := len(consensusConfig.DifficultyAdjustmentWindowSize)
		consensusConfig.DifficultyAdjustmentWindowSize = make([]int, versions)
		for i := range versions {
			consensusConfig.DifficultyAdjustmentWindowSize[i] = 10_000
		}
		consensusConfig.DifficultyAdjustmentWindowSize[0] = 10

		tc, teardown, err := consensus.NewFactory().NewTestConsensus(consensusConfig, "TestDAABlockWindowOwnVersion")
		if err != nil {
			t.Fatalf("NewTestConsensus: %+v", err)
		}
		defer teardown(false)

		tip := consensusConfig.GenesisHash
		for i := range 40 {
			tip, _, err = tc.AddBlock([]*externalapi.DomainHash{tip}, nil, nil)
			if err != nil {
				t.Fatalf("AddBlock #%d: %+v", i, err)
			}
		}

		windowAt := func(globalVersion uint) int {
			constants.ForceSetBlockVersion(globalVersion)
			defer constants.ForceSetBlockVersion(1)
			window, err := tc.DAGTraversalManager().DAABlockWindow(model.NewStagingArea(), tip)
			if err != nil {
				t.Fatalf("DAABlockWindow with global %d: %+v", globalVersion, err)
			}
			return len(window)
		}
		if atOne, atNine := windowAt(1), windowAt(9); atOne != atNine {
			t.Fatalf("the same version-1 block has a %d-block DAA window with the global at 1 and %d at 9", atOne, atNine)
		}
	})
}
