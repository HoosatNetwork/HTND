package consensusstatemanager_test

import (
	"testing"

	"github.com/Hoosat-Oy/HTND/domain/consensus/model"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/consensushashing"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/utxo"

	"github.com/Hoosat-Oy/HTND/domain/consensus"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/testutils"
)

func TestVirtualDiff(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		factory := consensus.NewFactory()
		tc, teardown, err := factory.NewTestConsensus(consensusConfig, "TestVirtualDiff")
		if err != nil {
			t.Fatalf("Error setting up tc: %+v", err)
		}
		defer teardown(false)

		// Add block A over the genesis
		blockAHash, virtualChangeSet, err := tc.AddBlock([]*externalapi.DomainHash{consensusConfig.GenesisHash}, nil, nil)
		if err != nil {
			t.Fatalf("Error adding block A: %+v", err)
		}

		virtualUTXODiff := virtualChangeSet.VirtualUTXODiff
		if virtualUTXODiff.ToRemove().Len() != 0 {
			t.Fatalf("Unexpected length %d for virtualUTXODiff.ToRemove()", virtualUTXODiff.ToRemove().Len())
		}

		// Because the genesis is not in block A's DAA window, block A's coinbase doesn't pay to it, so it has no outputs.
		if virtualUTXODiff.ToAdd().Len() != 0 {
			t.Fatalf("Unexpected length %d for virtualUTXODiff.ToAdd()", virtualUTXODiff.ToAdd().Len())
		}

		blockBHash, virtualChangeSet, err := tc.AddBlock([]*externalapi.DomainHash{blockAHash}, nil, nil)
		if err != nil {
			t.Fatalf("Error adding block A: %+v", err)
		}

		blockB, err := tc.BlockStore().Block(tc.DatabaseContext(), model.NewStagingArea(), blockBHash)
		if err != nil {
			t.Fatalf("Block: %+v", err)
		}

		virtualUTXODiff = virtualChangeSet.VirtualUTXODiff
		if virtualUTXODiff.ToRemove().Len() != 0 {
			t.Fatalf("Unexpected length %d for virtualUTXODiff.ToRemove()", virtualUTXODiff.ToRemove().Len())
		}

		expectedOutputs := blockB.Transactions[0].Outputs
		if virtualUTXODiff.ToAdd().Len() != len(expectedOutputs) {
			t.Fatalf("Unexpected length %d for virtualUTXODiff.ToAdd()", virtualUTXODiff.ToAdd().Len())
		}

		for i, output := range expectedOutputs {
			outpoint := &externalapi.DomainOutpoint{
				TransactionID: *consensushashing.TransactionID(blockB.Transactions[0]),
				Index:         uint32(i),
			}
			entry, ok := virtualUTXODiff.ToAdd().Get(outpoint)
			if !ok {
				t.Fatalf("Missing outpoint %s", outpoint)
			}

			if !entry.Equal(utxo.NewUTXOEntry(
				output.Value,
				output.ScriptPublicKey,
				true,
				blockB.Header.DAAScore()+1,
			)) {
				t.Fatalf("Unexpected entry %s", entry)
			}
		}
	})
}
