package consensus_test

import (
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus"
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/util/staging"
)

// TestRepairDisqualifiedTipChainsResetsOnlyTheDisqualifiedPrefix builds a chain, marks the blocks
// nearest the tip disqualified and leaves the one below them UTXO-valid, and requires the repair to
// reset exactly the disqualified ones and stop.
//
// Both halves matter. Resetting past the first non-disqualified block would make the repair as blunt
// as the whole-store one; and resetting to StatusUTXOValid rather than StatusUTXOPendingVerification
// would leave blocks that resolveBlockStatus takes at their word and never gives a UTXO diff to.
func TestRepairDisqualifiedTipChainsResetsOnlyTheDisqualifiedPrefix(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		factory := consensus.NewFactory()
		tc, teardown, err := factory.NewTestConsensus(consensusConfig,
			"TestRepairDisqualifiedTipChainsResetsOnlyTheDisqualifiedPrefix")
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

		// The two blocks nearest the tip are disqualified; the one below them keeps its real status,
		// so it is where the walk has to stop.
		disqualified := []*externalapi.DomainHash{chain[len(chain)-1], chain[len(chain)-2]}
		boundary := chain[len(chain)-3]

		stagingArea := model.NewStagingArea()
		boundaryStatusBefore, err := tc.BlockStatusStore().Get(tc.DatabaseContext(), stagingArea, boundary)
		if err != nil {
			t.Fatalf("Get status of the boundary block: %+v", err)
		}
		for _, blockHash := range disqualified {
			tc.BlockStatusStore().Stage(stagingArea, blockHash, externalapi.StatusDisqualifiedFromChain)
		}
		if err := staging.CommitAllChanges(tc.DatabaseContext(), stagingArea); err != nil {
			t.Fatalf("CommitAllChanges: %+v", err)
		}

		resetCount, err := tc.RepairDisqualifiedTipChains()
		if err != nil {
			t.Fatalf("RepairDisqualifiedTipChains: %+v", err)
		}
		if resetCount != uint64(len(disqualified)) {
			t.Fatalf("expected %d blocks to be reset, got %d", len(disqualified), resetCount)
		}

		readingArea := model.NewStagingArea()
		for _, blockHash := range disqualified {
			status, err := tc.BlockStatusStore().Get(tc.DatabaseContext(), readingArea, blockHash)
			if err != nil {
				t.Fatalf("Get status of %s: %+v", blockHash, err)
			}
			if status != externalapi.StatusUTXOPendingVerification {
				t.Fatalf("expected %s to be reset to %s so it is resolved again and gets a UTXO diff, but it is %s",
					blockHash, externalapi.StatusUTXOPendingVerification, status)
			}
		}

		boundaryStatusAfter, err := tc.BlockStatusStore().Get(tc.DatabaseContext(), readingArea, boundary)
		if err != nil {
			t.Fatalf("Get status of the boundary block: %+v", err)
		}
		if boundaryStatusAfter != boundaryStatusBefore {
			t.Fatalf("the walk passed the first block that was not disqualified: %s went from %s to %s",
				boundary, boundaryStatusBefore, boundaryStatusAfter)
		}
	})
}

// A DAG with nothing disqualified must come back untouched, so the caller can tell "the statuses were
// the problem" from "the tips are invalid and resetting statuses cannot help".
func TestRepairDisqualifiedTipChainsResetsNothingWhenNoTipIsDisqualified(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		factory := consensus.NewFactory()
		tc, teardown, err := factory.NewTestConsensus(consensusConfig,
			"TestRepairDisqualifiedTipChainsResetsNothingWhenNoTipIsDisqualified")
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

		resetCount, err := tc.RepairDisqualifiedTipChains()
		if err != nil {
			t.Fatalf("RepairDisqualifiedTipChains: %+v", err)
		}
		if resetCount != 0 {
			t.Fatalf("expected a healthy DAG to be left alone, but %d blocks were reset", resetCount)
		}
	})
}
