package consensusstatemanager_test

import (
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus"
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/multiset"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/util/staging"
)

// TestChildOfDisqualifiedParentOnOffsetBaseline pins that a node on a known-offset UTXO baseline
// verifies each child of a disqualified block on its own, instead of disqualifying the whole chain
// above it by inheritance.
//
// On such a node verifyUTXO tolerates an inherited commitment offset for every block whose own
// acceptance data and UTXO diff agree. ResolveBlockStatus's inheritance branch never called
// verifyUTXO, so one disqualification - reached for any local reason - disqualified every later
// block on the chain unchecked, and the node pinned virtual below the network's tip for good.
//
// The strict case is the control: with a verified baseline the cascade is kept, exactly as before.
func TestChildOfDisqualifiedParentOnOffsetBaseline(t *testing.T) {
	for _, offsetBaseline := range []bool{true, false} {
		name := "verified baseline keeps the cascade"
		if offsetBaseline {
			name = "offset baseline verifies each child"
		}
		t.Run(name, func(t *testing.T) {
			testChildOfDisqualifiedParent(t, offsetBaseline)
		})
	}
}

func testChildOfDisqualifiedParent(t *testing.T, offsetBaseline bool) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		factory := consensus.NewFactory()

		builder, teardownBuilder, err := factory.NewTestConsensus(consensusConfig,
			"TestChildOfDisqualifiedParentOnOffsetBaseline_builder")
		if err != nil {
			t.Fatalf("Error setting up builder consensus: %+v", err)
		}
		defer teardownBuilder(false)

		const chainLength = 8
		chain := make([]*externalapi.DomainHash, 0, chainLength)
		tipHash := consensusConfig.GenesisHash
		for i := range chainLength {
			tipHash, _, err = builder.AddBlock([]*externalapi.DomainHash{tipHash}, nil, nil)
			if err != nil {
				t.Fatalf("Error adding block %d to the builder: %+v", i, err)
			}
			chain = append(chain, tipHash)
		}

		tc, teardown, err := factory.NewTestConsensus(consensusConfig, "TestChildOfDisqualifiedParentOnOffsetBaseline")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		for i, blockHash := range chain {
			block, found, err := builder.GetBlock(blockHash)
			if err != nil || !found {
				t.Fatalf("Error getting block %d from the builder: found=%t err=%+v", i, found, err)
			}
			// chain[0] and chain[1] are resolved on arrival, everything above them is left pending.
			err = tc.ValidateAndInsertBlock(block, i <= 1, true)
			if err != nil {
				t.Fatalf("Error inserting block %d: %+v", i, err)
			}
		}

		stagingArea := model.NewStagingArea()
		if offsetBaseline {
			// Make chain[0] the pruning point and give it a stored multiset that does not hash to its
			// header's commitment: pruningPointBaselineIsOffset's signal, as an imported offset set
			// leaves it.
			err = tc.PruningStore().StagePruningPoint(tc.DatabaseContext(), stagingArea, chain[0])
			if err != nil {
				t.Fatalf("Error staging the pruning point: %+v", err)
			}
			wrongMultiset := multiset.New()
			wrongMultiset.Add([]byte("an entry the network's set does not hold"))
			tc.MultisetStore().Stage(stagingArea, chain[0], wrongMultiset)
		}
		// A disqualification this node reached on its own, below every pending block.
		tc.BlockStatusStore().Stage(stagingArea, chain[1], externalapi.StatusDisqualifiedFromChain)
		err = staging.CommitAllChanges(tc.DatabaseContext(), stagingArea)
		if err != nil {
			t.Fatalf("Error staging the disqualification: %+v", err)
		}

		for step := 0; !virtualSelectedParent(t, tc).Equal(tipHash); step++ {
			if step > chainLength {
				t.Fatalf("virtual did not reach the chain tip after %d resolutions", step)
			}
			_, _, err = tc.ResolveVirtualWithMaxParam(3)
			if err != nil {
				t.Fatalf("Error resolving virtual: %+v", err)
			}
		}

		parentStatus, err := tc.BlockStatusStore().Get(tc.DatabaseContext(), model.NewStagingArea(), chain[1])
		if err != nil {
			t.Fatalf("Error getting the disqualified block's status: %+v", err)
		}
		if parentStatus != externalapi.StatusDisqualifiedFromChain {
			t.Fatalf("the disqualified block must keep its own status, got %s", parentStatus)
		}

		expected := externalapi.StatusDisqualifiedFromChain
		if offsetBaseline {
			expected = externalapi.StatusUTXOValid
		}
		for i := 2; i < chainLength; i++ {
			status, err := tc.BlockStatusStore().Get(tc.DatabaseContext(), model.NewStagingArea(), chain[i])
			if err != nil {
				t.Fatalf("Error getting the status of block %d: %+v", i, err)
			}
			if status != expected {
				t.Fatalf("block %d: expected %s, got %s", i, expected, status)
			}
		}
	})
}
