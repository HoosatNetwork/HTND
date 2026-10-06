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

// TestGHOSTDAGColoringFollowsTheBlocksOwnVersion pins that a block is colored by the GHOSTDAG rules of its own era,
// whatever the process-global block version happens to be. Dynamic K (from version 6) and the enlarged anticone
// bound (from version 7) were selected by the global, which a restart resets to 1 and IBD raises to the tip version, so
// the same block could be colored differently on different nodes - and GHOSTDAG data is stored and never recomputed.
func TestGHOSTDAGColoringFollowsTheBlocksOwnVersion(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		defer constants.ForceSetBlockVersion(1)

		// Every block is version 1, where K is 0: a block merging two parallel blocks colors the side one red.
		consensusConfig.POWScores = []uint64{math.MaxUint64}
		consensusConfig.K = append([]externalapi.KType(nil), consensusConfig.K...)
		consensusConfig.K[0] = 0

		tc, teardown, err := consensus.NewFactory().NewTestConsensus(consensusConfig, "TestGHOSTDAGColoringOwnVersion")
		if err != nil {
			t.Fatalf("NewTestConsensus: %+v", err)
		}
		defer teardown(false)

		addBlock := func(parents ...*externalapi.DomainHash) *externalapi.DomainHash {
			hash, _, err := tc.AddBlock(parents, nil, nil)
			if err != nil {
				t.Fatalf("AddBlock: %+v", err)
			}
			return hash
		}
		base := addBlock(consensusConfig.GenesisHash)
		left := addBlock(base)
		right := addBlock(base)
		merge := addBlock(left, right)

		stored, err := tc.GHOSTDAGDataStore().Get(tc.DatabaseContext(), model.NewStagingArea(), merge, false)
		if err != nil {
			t.Fatalf("GHOSTDAGDataStore.Get: %+v", err)
		}
		if len(stored.MergeSetReds()) != 1 {
			t.Fatalf("setup: with K=0 the merge block should have one red, got %d blues and %d reds",
				len(stored.MergeSetBlues()), len(stored.MergeSetReds()))
		}

		// A node whose global version is 9 (running a while, or after IBD) recolors the same block.
		constants.ForceSetBlockVersion(9)
		stagingArea := model.NewStagingArea()
		if err := tc.GHOSTDAGManager().GHOSTDAG(stagingArea, merge); err != nil {
			t.Fatalf("GHOSTDAG: %+v", err)
		}
		recolored, err := tc.GHOSTDAGDataStore().Get(tc.DatabaseContext(), stagingArea, merge, false)
		if err != nil {
			t.Fatalf("GHOSTDAGDataStore.Get: %+v", err)
		}
		constants.ForceSetBlockVersion(1)

		if len(recolored.MergeSetBlues()) != len(stored.MergeSetBlues()) ||
			len(recolored.MergeSetReds()) != len(stored.MergeSetReds()) ||
			recolored.BlueScore() != stored.BlueScore() {
			t.Fatalf("the same version-1 block colored differently with the global at 9: %d blues/%d reds/blue score %d "+
				"vs %d/%d/%d at 1", len(recolored.MergeSetBlues()), len(recolored.MergeSetReds()), recolored.BlueScore(),
				len(stored.MergeSetBlues()), len(stored.MergeSetReds()), stored.BlueScore())
		}
	})
}
