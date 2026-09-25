package consensus_test

import (
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/multiset"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
)

// TestVirtualTableRestampsTheSelectedParentsCoinbase is the HTN-005 repro attempt for the wrong stamps found on a
// mainnet datadir: coinbase outputs of a chain block's selected parent held in virtual's UTXO table at virtual's DAA
// score rather than the DAA score of the chain child that accepted them.
//
// While P is virtual's selected parent and virtual also merges a sibling tip X, virtual's DAA score runs ahead and
// virtual accepts P's coinbase at that score. When B, a child of P only, becomes the selected tip, B accepts P's
// coinbase at B's own DAA score, so the materialised table must be restamped to it - and the table must still hash to
// virtual's stored multiset.
func TestVirtualTableRestampsTheSelectedParentsCoinbase(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		tc, teardown, err := consensus.NewFactory().NewTestConsensus(consensusConfig, "TestVirtualTableRestampsCoinbase")
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
		daaScoreOf := func(hash *externalapi.DomainHash) uint64 {
			header, err := tc.GetBlockHeader(hash)
			if err != nil {
				t.Fatalf("GetBlockHeader: %+v", err)
			}
			return header.DAAScore()
		}
		tableStamp := func(outpoint *externalapi.DomainOutpoint) (uint64, bool) {
			held, err := tc.ConsensusStateStore().HasUTXOByOutpoint(tc.DatabaseContext(), model.NewStagingArea(), outpoint)
			if err != nil {
				t.Fatalf("HasUTXOByOutpoint: %+v", err)
			}
			if !held {
				return 0, false
			}
			entry, found, err := tc.ConsensusStateStore().UTXOByOutpoint(tc.DatabaseContext(), model.NewStagingArea(), outpoint)
			if err != nil {
				t.Fatalf("UTXOByOutpoint: %+v", err)
			}
			if !found {
				return 0, false
			}
			return entry.BlockDAAScore(), true
		}

		tip := consensusConfig.GenesisHash
		for range 5 {
			tip = addBlock(tip)
		}
		first, second := addBlock(tip), addBlock(tip)

		selectedParent, err := tc.GetVirtualSelectedParent()
		if err != nil {
			t.Fatalf("GetVirtualSelectedParent: %+v", err)
		}
		if !selectedParent.Equal(first) && !selectedParent.Equal(second) {
			t.Fatalf("setup: virtual's selected parent %s is neither sibling", selectedParent)
		}
		virtualDAAScoreBefore, err := tc.GetVirtualDAAScore()
		if err != nil {
			t.Fatalf("GetVirtualDAAScore: %+v", err)
		}
		parentBlock, _, err := tc.GetBlock(selectedParent)
		if err != nil {
			t.Fatalf("GetBlock: %+v", err)
		}
		coinbase := parentBlock.Transactions[0]
		if len(coinbase.Outputs) == 0 {
			t.Fatalf("setup: the selected parent's coinbase has no outputs")
		}
		outpoint := externalapi.NewDomainOutpoint(consensushashing.TransactionID(coinbase), 0)
		stampBefore, heldBefore := tableStamp(outpoint)
		t.Logf("selected parent %s (DAA %d), virtual DAA %d, its coinbase output in the table: held=%t stamp=%d",
			selectedParent, daaScoreOf(selectedParent), virtualDAAScoreBefore, heldBefore, stampBefore)

		child := addBlock(selectedParent)
		newSelectedParent, err := tc.GetVirtualSelectedParent()
		if err != nil {
			t.Fatalf("GetVirtualSelectedParent: %+v", err)
		}
		if !newSelectedParent.Equal(child) {
			t.Fatalf("setup: the chain child %s did not become virtual's selected parent (%s did)", child, newSelectedParent)
		}
		childDAAScore := daaScoreOf(child)
		stampAfter, heldAfter := tableStamp(outpoint)
		virtualDAAScoreAfter, err := tc.GetVirtualDAAScore()
		if err != nil {
			t.Fatalf("GetVirtualDAAScore: %+v", err)
		}
		t.Logf("chain child %s (DAA %d), virtual DAA %d, coinbase output in the table: held=%t stamp=%d",
			child, childDAAScore, virtualDAAScoreAfter, heldAfter, stampAfter)

		if !heldAfter {
			t.Fatalf("the selected parent's coinbase output is missing from virtual's table after its chain child accepted it")
		}
		if stampAfter != childDAAScore {
			t.Errorf("virtual's table holds the selected parent's coinbase output at %d; the chain child that accepted "+
				"it has DAA score %d (virtual's score while it was the selected parent: %d)",
				stampAfter, childDAAScore, virtualDAAScoreBefore)
		}

		iterator, err := tc.ConsensusStateStore().VirtualUTXOSetIterator(tc.DatabaseContext(), model.NewStagingArea())
		if err != nil {
			t.Fatalf("VirtualUTXOSetIterator: %+v", err)
		}
		defer iterator.Close()
		table := multiset.New()
		for ok := iterator.First(); ok; ok = iterator.Next() {
			tableOutpoint, entry, err := iterator.Get()
			if err != nil {
				t.Fatalf("iterator.Get: %+v", err)
			}
			serialized, err := utxo.SerializeUTXO(entry, tableOutpoint)
			if err != nil {
				t.Fatalf("SerializeUTXO: %+v", err)
			}
			table.Add(serialized)
		}
		virtualMultiset, err := tc.MultisetStore().Get(tc.DatabaseContext(), model.NewStagingArea(), model.VirtualBlockHash)
		if err != nil {
			t.Fatalf("virtual multiset: %+v", err)
		}
		if !table.Hash().Equal(virtualMultiset.Hash()) {
			t.Errorf("virtual's UTXO table hashes to %s but virtual's stored multiset is %s",
				table.Hash(), virtualMultiset.Hash())
		}
	})
}
