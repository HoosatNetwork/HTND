package consensus_test

import (
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/v2/util/staging"
)

// TestRepairMissingMultisetsResetsOnlyTheBlocksMissingOne builds a chain, deletes the multisets of
// the two blocks nearest the tip (reproducing what RepairBlockStatuses can leave behind - a
// StatusUTXOValid block with no stored multiset) and leaves the one below them intact, and requires
// the repair to reset exactly the blocks missing a multiset and stop at the first one that still has
// one.
//
// Both halves matter, the same way they do for RepairDisqualifiedTipChains: resetting past the first
// block that already has a multiset would make this as blunt as re-running RepairBlockStatuses
// itself; and resetting to StatusUTXOValid rather than StatusUTXOPendingVerification would leave the
// block exactly as broken as before - taken at its word by resolveBlockStatus and never given a real
// diff/multiset.
func TestRepairMissingMultisetsResetsOnlyTheBlocksMissingOne(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		factory := consensus.NewFactory()
		tc, teardown, err := factory.NewTestConsensus(consensusConfig,
			"TestRepairMissingMultisetsResetsOnlyTheBlocksMissingOne")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		chain := []*externalapi.DomainHash{consensusConfig.GenesisHash}
		for range 4 {
			blockHash, _, err := tc.AddBlock([]*externalapi.DomainHash{chain[len(chain)-1]}, nil, nil)
			if err != nil {
				t.Fatalf("AddBlock: %+v", err)
			}
			chain = append(chain, blockHash)
		}

		// The two blocks nearest the tip lose their multiset; the one below them keeps its real one,
		// so it is where the walk has to stop.
		missingMultiset := []*externalapi.DomainHash{chain[len(chain)-1], chain[len(chain)-2]}
		boundary := chain[len(chain)-3]

		stagingArea := model.NewStagingArea()
		boundaryStatusBefore, err := tc.BlockStatusStore().Get(tc.DatabaseContext(), stagingArea, boundary)
		if err != nil {
			t.Fatalf("Get status of the boundary block: %+v", err)
		}
		for _, blockHash := range missingMultiset {
			tc.MultisetStore().Delete(stagingArea, blockHash)
		}
		if err := staging.CommitAllChanges(tc.DatabaseContext(), stagingArea); err != nil {
			t.Fatalf("CommitAllChanges: %+v", err)
		}

		resetCount, err := tc.RepairMissingMultisets()
		if err != nil {
			t.Fatalf("RepairMissingMultisets: %+v", err)
		}
		if resetCount != uint64(len(missingMultiset)) {
			t.Fatalf("expected %d blocks to be reset, got %d", len(missingMultiset), resetCount)
		}

		readingArea := model.NewStagingArea()
		for _, blockHash := range missingMultiset {
			status, err := tc.BlockStatusStore().Get(tc.DatabaseContext(), readingArea, blockHash)
			if err != nil {
				t.Fatalf("Get status of %s: %+v", blockHash, err)
			}
			if status != externalapi.StatusUTXOPendingVerification {
				t.Fatalf("expected %s to be reset to %s so it is resolved again and gets a real multiset, but it is %s",
					blockHash, externalapi.StatusUTXOPendingVerification, status)
			}
		}

		boundaryStatusAfter, err := tc.BlockStatusStore().Get(tc.DatabaseContext(), readingArea, boundary)
		if err != nil {
			t.Fatalf("Get status of the boundary block: %+v", err)
		}
		if boundaryStatusAfter != boundaryStatusBefore {
			t.Fatalf("the walk passed the first block that already had a multiset: %s went from %s to %s",
				boundary, boundaryStatusBefore, boundaryStatusAfter)
		}
	})
}

// A DAG where every block still has its multiset must come back untouched, so the caller can tell
// "a multiset was actually missing" from "nothing needed repairing".
func TestRepairMissingMultisetsResetsNothingWhenEveryMultisetExists(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		factory := consensus.NewFactory()
		tc, teardown, err := factory.NewTestConsensus(consensusConfig,
			"TestRepairMissingMultisetsResetsNothingWhenEveryMultisetExists")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		current := consensusConfig.GenesisHash
		for range 3 {
			current, _, err = tc.AddBlock([]*externalapi.DomainHash{current}, nil, nil)
			if err != nil {
				t.Fatalf("AddBlock: %+v", err)
			}
		}

		resetCount, err := tc.RepairMissingMultisets()
		if err != nil {
			t.Fatalf("RepairMissingMultisets: %+v", err)
		}
		if resetCount != 0 {
			t.Fatalf("expected a healthy DAG to be left alone, but %d blocks were reset", resetCount)
		}
	})
}
