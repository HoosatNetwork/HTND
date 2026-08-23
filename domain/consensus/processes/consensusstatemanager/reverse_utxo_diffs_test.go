package consensusstatemanager_test

import (
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"

	"github.com/HoosatNetwork/HTND/domain/consensus/model"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"

	"github.com/HoosatNetwork/HTND/domain/consensus"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/testutils"
)

func TestReverseUTXODiffs(t *testing.T) {
	// This test doesn't check ReverseUTXODiffs directly.
	// It creates a 5-block chain and then a longer reorg chain,
	// then verifies that the UTXODiffChild pointers form a correct chain
	// after the reorg (the tip points to virtual, every other block
	// points to the next block in the reorg chain).

	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		factory := consensus.NewFactory()

		tc, teardown, err := factory.NewTestConsensus(consensusConfig, "TestReverseUTXODiffs")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		// This test mines two competing chains from genesis, so blocks at
		// matching heights would otherwise share both blue score and default
		// coinbase data and collide on their coinbase transaction ID.
		tc.BlockBuilder().EnableUniqueDefaultCoinbaseExtraData()

		// Create a chain of 5 blocks
		const initialChainLength = 5
		previousBlockHash := consensusConfig.GenesisHash
		for i := range initialChainLength {
			previousBlockHash, _, err = tc.AddBlock([]*externalapi.DomainHash{previousBlockHash}, nil, nil)
			if err != nil {
				t.Fatalf("Error mining block no. %d in initial chain: %+v", i, err)
			}
		}

		// Mine a longer chain that causes a reorg
		const reorgChainLength = initialChainLength + 1
		reorgChain := make([]*externalapi.DomainHash, reorgChainLength)
		previousBlockHash = consensusConfig.GenesisHash
		for i := range reorgChainLength {
			previousBlockHash, _, err = tc.AddBlock([]*externalapi.DomainHash{previousBlockHash}, nil, nil)
			if err != nil {
				t.Fatalf("Error mining block no. %d in re-org chain: %+v", i, err)
			}
			reorgChain[i] = previousBlockHash
		}

		stagingArea := model.NewStagingArea()

		// Verify the UTXODiffChild chain after the reorg
		for i, currentBlockHash := range reorgChain {
			if i == reorgChainLength-1 {
				// Tip should not have a UTXODiffChild (it points to virtual)
				hasUTXODiffChild, err := tc.UTXODiffStore().HasUTXODiffChild(
					tc.DatabaseContext(), stagingArea, currentBlockHash)
				if err != nil {
					t.Fatalf("Error getting HasUTXODiffChild of %s: %+v", currentBlockHash, err)
				}
				if hasUTXODiffChild {
					t.Errorf("Block %s (tip) expected to have no UTXODiffChild (virtual), "+
						"but HasUTXODiffChild returned true", currentBlockHash)
				}
			} else {
				utxoDiffChild, err := tc.UTXODiffStore().UTXODiffChild(
					tc.DatabaseContext(), stagingArea, currentBlockHash)
				if err != nil {
					t.Fatalf("Error getting utxoDiffChild of block %d (%s): %+v",
						i, currentBlockHash, err)
				}
				expected := reorgChain[i+1]
				if !expected.Equal(utxoDiffChild) {
					t.Errorf("Block %s expected UTXODiffChild %s, got %s",
						currentBlockHash, expected, utxoDiffChild)
				}
			}
		}
	})
}

// isUTXODiffOnlyRemoveCoinbase returns true when the diff contains
// exactly one entry in toRemove that is the coinbase of the given block
// (outpoint = txid:0) and toAdd is empty.
func isUTXODiffOnlyRemoveCoinbase(diff externalapi.UTXODiff, block *externalapi.DomainBlock) bool {
	if diff.ToAdd().Len() != 0 {
		return false
	}

	coinbaseTx := block.Transactions[0]
	coinbaseTxID := *consensushashing.TransactionID(coinbaseTx)
	expectedCount := len(coinbaseTx.Outputs)

	if diff.ToRemove().Len() != expectedCount {
		return false
	}

	// Verify that every removed outpoint belongs to this coinbase
	for i := range expectedCount {
		outpoint := externalapi.DomainOutpoint{
			TransactionID: coinbaseTxID,
			Index:         uint32(i),
		}
		if _, ok := diff.ToRemove().Get(&outpoint); !ok {
			return false
		}
	}

	return true
}

func checkIsUTXODiffOnlyRemoveCoinbase(t *testing.T, utxoDiff externalapi.UTXODiff, currentBlock *externalapi.DomainBlock) bool {
	if len(currentBlock.Transactions[0].Outputs) == 0 {
		return utxoDiff.ToAdd().Len() == 0 && utxoDiff.ToRemove().Len() == 0
	}

	coinbaseTxID := consensushashing.TransactionID(currentBlock.Transactions[0])
	coinbaseOutputsCount := len(currentBlock.Transactions[0].Outputs)

	if utxoDiff.ToAdd().Len() != 0 {
		return false
	}
	if utxoDiff.ToRemove().Len() != coinbaseOutputsCount {
		return false
	}

	for outputIndex := range coinbaseOutputsCount {
		outpoint := &externalapi.DomainOutpoint{TransactionID: *coinbaseTxID, Index: uint32(outputIndex)}
		if !utxoDiff.ToRemove().Contains(outpoint) {
			return false
		}
	}

	iterator := utxoDiff.ToRemove().Iterator()
	defer iterator.Close()
	for ok := iterator.First(); ok; ok = iterator.Next() {
		outpoint, _, err := iterator.Get()
		if err != nil {
			t.Fatalf("Error getting from UTXODiff's iterator: %+v", err)
		}
		if !outpoint.TransactionID.Equal(coinbaseTxID) {
			return false
		}
	}

	return true
}
