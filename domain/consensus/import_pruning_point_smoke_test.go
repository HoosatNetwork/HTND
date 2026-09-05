package consensus_test

import (
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/testutils"
)

// TestImportPruningPointUTXOSetSmoke is a focused smoke test for the exact sequence of public
// externalapi.Consensus calls `htnexodus import` uses to rebaseline a node onto a candidate
// exodus bundle (ClearImportedPruningPointData -> AppendImportedPruningPointUTXOs ->
// ValidateAndInsertImportedPruningPoint), confirming that:
//   - it succeeds when applied to a block the consensus already fully knows (its own recent
//     selected-parent-chain tip), using UTXO entries captured via IterateUTXOSetAtBlock (the
//     same API `htnexodus create` uses to build a bundle);
//   - after import, virtual's selected parent is forced to that block and its UTXO set matches
//     what was imported.
func TestImportPruningPointUTXOSetSmoke(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		factory := consensus.NewFactory()
		tc, teardown, err := factory.NewTestConsensus(consensusConfig, "TestImportPruningPointUTXOSetSmoke")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		blockHash, _, err := tc.AddBlock([]*externalapi.DomainHash{consensusConfig.GenesisHash}, nil, nil)
		if err != nil {
			t.Fatalf("AddBlock: %+v", err)
		}

		// Capture the target block's UTXO set exactly like `htnexodus create` would.
		var pairs []*externalapi.OutpointAndUTXOEntryPair
		err = tc.IterateUTXOSetAtBlock(blockHash, func(outpoint *externalapi.DomainOutpoint, entry externalapi.UTXOEntry) error {
			pairs = append(pairs, &externalapi.OutpointAndUTXOEntryPair{Outpoint: outpoint, UTXOEntry: entry})
			return nil
		})
		if err != nil {
			t.Fatalf("IterateUTXOSetAtBlock: %+v", err)
		}
		t.Logf("pairs at target block: %d", len(pairs))

		// Mine further so that virtual's selected parent is no longer the target block, exactly
		// like a real "rebaseline onto an older-than-tip block" scenario.
		_, _, err = tc.AddBlock([]*externalapi.DomainHash{blockHash}, nil, nil)
		if err != nil {
			t.Fatalf("AddBlock: %+v", err)
		}

		// Exercise the exact sequence `htnexodus import` performs.
		err = tc.ClearImportedPruningPointData()
		if err != nil {
			t.Fatalf("ClearImportedPruningPointData: %+v", err)
		}
		err = tc.AppendImportedPruningPointUTXOs(pairs)
		if err != nil {
			t.Fatalf("AppendImportedPruningPointUTXOs: %+v", err)
		}
		err = tc.ValidateAndInsertImportedPruningPoint(blockHash)
		if err != nil {
			t.Fatalf("ValidateAndInsertImportedPruningPoint: %+v", err)
		}

		newVirtualSelectedParent, err := tc.GetVirtualSelectedParent()
		if err != nil {
			t.Fatalf("GetVirtualSelectedParent: %+v", err)
		}
		if !newVirtualSelectedParent.Equal(blockHash) {
			t.Fatalf("expected virtual selected parent to be forced to %s, got %s", blockHash, newVirtualSelectedParent)
		}

		virtualUTXOs, err := tc.GetVirtualUTXOs([]*externalapi.DomainHash{newVirtualSelectedParent}, nil, 0)
		if err != nil {
			t.Fatalf("GetVirtualUTXOs: %+v", err)
		}
		if len(virtualUTXOs) != len(pairs) {
			t.Fatalf("expected virtual UTXO set to match the imported entry count (%d), got %d",
				len(pairs), len(virtualUTXOs))
		}

		blockInfo, err := tc.GetBlockInfo(blockHash)
		if err != nil {
			t.Fatalf("GetBlockInfo: %+v", err)
		}
		if blockInfo.BlockStatus != externalapi.StatusUTXOValid {
			t.Fatalf("expected imported pruning point block to be StatusUTXOValid, got %s", blockInfo.BlockStatus)
		}
	})
}
