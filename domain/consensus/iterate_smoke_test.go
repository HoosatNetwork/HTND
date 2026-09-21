package consensus_test

import (
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/testutils"
)

// TestIterateUTXOSetAtBlockSmoke is a focused smoke test for the new
// externalapi.Consensus.IterateUTXOSetAtBlock API (used by the exodus pruning point candidate
// tooling to walk the UTXO set of an arbitrary historical block, not just the current pruning
// point or virtual). It confirms the API can be used against both the genesis block and a
// freshly mined descendant without error, and that it agrees with GetVirtualUTXOs for the
// current tip.
func TestIterateUTXOSetAtBlockSmoke(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		factory := consensus.NewFactory()
		tc, teardown, err := factory.NewTestConsensus(consensusConfig, "TestIterateUTXOSetAtBlockSmoke")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		countAt := func(blockHash *externalapi.DomainHash) int {
			count := 0
			err := tc.IterateUTXOSetAtBlock(blockHash, func(outpoint *externalapi.DomainOutpoint, entry externalapi.UTXOEntry) error {
				count++
				return nil
			})
			if err != nil {
				t.Fatalf("IterateUTXOSetAtBlock: %+v", err)
			}
			return count
		}

		genesisCount := countAt(consensusConfig.GenesisHash)

		blockHash, _, err := tc.AddBlock([]*externalapi.DomainHash{consensusConfig.GenesisHash}, nil, nil)
		if err != nil {
			t.Fatalf("AddBlock: %+v", err)
		}

		tipCount := countAt(blockHash)

		virtualParent, err := tc.GetVirtualSelectedParent()
		if err != nil {
			t.Fatalf("GetVirtualSelectedParent: %+v", err)
		}
		virtualUTXOs, err := tc.GetVirtualUTXOs([]*externalapi.DomainHash{virtualParent}, nil, 0)
		if err != nil {
			t.Fatalf("GetVirtualUTXOs: %+v", err)
		}

		if tipCount != len(virtualUTXOs) {
			t.Fatalf("IterateUTXOSetAtBlock at the tip (%d) disagrees with GetVirtualUTXOs (%d)",
				tipCount, len(virtualUTXOs))
		}

		// Re-querying the strictly earlier genesis block must still work and be unaffected by
		// mining a descendant block.
		if got := countAt(consensusConfig.GenesisHash); got != genesisCount {
			t.Fatalf("genesis UTXO count changed after mining a child block: was %d, now %d", genesisCount, got)
		}
	})
}
