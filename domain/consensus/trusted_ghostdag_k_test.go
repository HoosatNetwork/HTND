package consensus_test

import (
	"math"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/testutils"
)

// TestTrustedGHOSTDAGContextFollowsTheBlocksOwnK pins that the selected parent chain served as a trusted block's
// GHOSTDAG context is sized by the K that block's version uses, not by the process-global version: servers that had
// restarted and servers that had not handed syncing peers different amounts of context for the same block.
func TestTrustedGHOSTDAGContextFollowsTheBlocksOwnK(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		defer constants.ForceSetBlockVersion(1)

		// Every block is version 1 with K=3; later versions use a K longer than the chain.
		consensusConfig.POWScores = []uint64{math.MaxUint64}
		consensusConfig.K = append([]externalapi.KType(nil), consensusConfig.K...)
		for i := range consensusConfig.K {
			consensusConfig.K[i] = 40
		}
		consensusConfig.K[0] = 3

		tc, teardown, err := consensus.NewFactory().NewTestConsensus(consensusConfig, "TestTrustedGHOSTDAGContextOwnK")
		if err != nil {
			t.Fatalf("NewTestConsensus: %+v", err)
		}
		defer teardown(false)

		tip := consensusConfig.GenesisHash
		for i := range 20 {
			tip, _, err = tc.AddBlock([]*externalapi.DomainHash{tip}, nil, nil)
			if err != nil {
				t.Fatalf("AddBlock #%d: %+v", i, err)
			}
		}

		contextAt := func(globalVersion uint) int {
			constants.ForceSetBlockVersion(globalVersion)
			defer constants.ForceSetBlockVersion(1)
			hashes, err := tc.PruningManager().TrustedBlockAssociatedGHOSTDAGDataBlockHashes(model.NewStagingArea(), tip)
			if err != nil {
				t.Fatalf("TrustedBlockAssociatedGHOSTDAGDataBlockHashes with global %d: %+v", globalVersion, err)
			}
			return len(hashes)
		}
		if atOne, atNine := contextAt(1), contextAt(9); atOne != atNine {
			t.Fatalf("the same version-1 block gets %d blocks of GHOSTDAG context with the global at 1 and %d at 9",
				atOne, atNine)
		}
	})
}
