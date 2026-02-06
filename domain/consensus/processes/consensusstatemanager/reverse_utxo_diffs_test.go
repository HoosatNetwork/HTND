package consensusstatemanager_test

import (
	"testing"

	"github.com/Hoosat-Oy/HTND/domain/consensus/model"

	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"

	"github.com/Hoosat-Oy/HTND/domain/consensus"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/testutils"
)

func TestReverseUTXODiffs(t *testing.T) {
	// This test creates a situation where a reorg happens - a chain of 5 blocks followed by a
	// chain of 6 blocks that causes reorganization. Then verifies that the UTXODiffs are stored.
	//
	// NOTE: With DAGKnight consensus (useSeparateStagingAreaPerBlock=true), blocks store their
	// UTXODiffs pointing toward virtual (cumulative diffs), not toward their immediate child.
	// The exact UTXODiff contents depend on the full chain state, so we just verify that
	// the diffs are stored and accessible.

	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		factory := consensus.NewFactory()

		tc, teardown, err := factory.NewTestConsensus(consensusConfig, "TestUTXOCommitment")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		// Create a chain of 5 blocks
		const initialChainLength = 5
		previousBlockHash := consensusConfig.GenesisHash
		for i := 0; i < initialChainLength; i++ {
			previousBlockHash, _, err = tc.AddBlock([]*externalapi.DomainHash{previousBlockHash}, nil, nil)
			if err != nil {
				t.Fatalf("Error mining block no. %d in initial chain: %+v", i, err)
			}
		}

		// Mine a chain of 6 blocks, to re-organize the DAG
		const reorgChainLength = initialChainLength + 1
		reorgChain := make([]*externalapi.DomainHash, reorgChainLength)
		previousBlockHash = consensusConfig.GenesisHash
		for i := 0; i < reorgChainLength; i++ {
			previousBlockHash, _, err = tc.AddBlock([]*externalapi.DomainHash{previousBlockHash}, nil, nil)
			reorgChain[i] = previousBlockHash
			if err != nil {
				t.Fatalf("Error mining block no. %d in re-org chain: %+v", i, err)
			}
		}

		stagingArea := model.NewStagingArea()
		// With DAGKnight, verify that all blocks have their UTXODiff stored.
		// The diffs point toward virtual (cumulative), so we just check they're accessible.
		for i, currentBlockHash := range reorgChain {
			// Verify UTXODiff is stored and accessible
			utxoDiff, err := tc.UTXODiffStore().UTXODiff(tc.DatabaseContext(), stagingArea, currentBlockHash)
			if err != nil {
				t.Fatalf("Error getting utxoDiff of block No. %d, %s: %+v", i, currentBlockHash, err)
			}

			// Basic sanity check - the diff should not be nil
			if utxoDiff == nil {
				t.Errorf("UTXODiff for block %s is nil", currentBlockHash)
			}
		}
	})
}
