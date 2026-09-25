package consensus_test

import (
	"math"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/testutils"
)

// TestRequiredDifficultyFollowsTheBlocksOwnVersion pins that the difficulty window size and target time used for a
// block are those of its own version. They were indexed by the process-global block version, so the same block's
// required difficulty (and the DAA data staged with it) depended on the node's uptime and IBD.
func TestRequiredDifficultyFollowsTheBlocksOwnVersion(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		defer constants.ForceSetBlockVersion(1)

		// Every block is version 1. Version 1 uses a short window and version 5+ a window longer than the chain, so
		// at version 5+ the window is never full and the required difficulty stays at the genesis bits.
		consensusConfig.POWScores = []uint64{math.MaxUint64}
		consensusConfig.DisableDifficultyAdjustment = false
		versions := len(consensusConfig.DifficultyAdjustmentWindowSize)
		consensusConfig.DifficultyAdjustmentWindowSize = make([]int, versions)
		consensusConfig.TargetTimePerBlock = append([]time.Duration(nil), consensusConfig.TargetTimePerBlock...)
		for i := range versions {
			consensusConfig.DifficultyAdjustmentWindowSize[i] = 10_000
		}
		consensusConfig.DifficultyAdjustmentWindowSize[0] = 10

		tc, teardown, err := consensus.NewFactory().NewTestConsensus(consensusConfig, "TestRequiredDifficultyOwnVersion")
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

		requiredAt := func(globalVersion uint) uint32 {
			constants.ForceSetBlockVersion(globalVersion)
			defer constants.ForceSetBlockVersion(1)
			bits, err := tc.DifficultyManager().RequiredDifficulty(model.NewStagingArea(), tip)
			if err != nil {
				t.Fatalf("RequiredDifficulty with global %d: %+v", globalVersion, err)
			}
			return bits
		}
		if atOne, atNine := requiredAt(1), requiredAt(9); atOne != atNine {
			t.Fatalf("the same version-1 block required different difficulty: %08x with the global at 1, %08x at 9",
				atOne, atNine)
		}
	})
}
