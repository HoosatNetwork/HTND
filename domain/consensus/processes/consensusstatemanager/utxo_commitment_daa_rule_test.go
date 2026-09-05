package consensusstatemanager_test

import (
	"math"
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus"
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/testapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/utxo"
)

// daaStampRule selects which DAA score gets stamped into the UTXOEntries a block's resolution
// creates.
type daaStampRule int

const (
	// mergingBlockRule stamps every UTXO created by any merge-set block with the DAA score of the
	// block that MERGED it (the block whose past UTXO is being resolved).
	mergingBlockRule daaStampRule = iota
	// creatingBlockRule stamps every UTXO with the DAA score of the merge-set block that CONTAINED
	// the transaction.
	creatingBlockRule
)

// TestUTXOCommitmentDAAStampRule reproduces, from a real consensus instance, the two candidate UTXO
// commitments for the same block, the same selected-parent multiset and the same acceptance data,
// differing ONLY in which DAA score is stamped into the created UTXOEntries. Exactly one of them may
// match the header the producer wrote; the other is the ErrBadUTXOCommitment "calculated value".
func TestUTXOCommitmentDAAStampRule(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		consensusConfig.BlockCoinbaseMaturity = 0
		factory := consensus.NewFactory()
		tc, teardown, err := factory.NewTestConsensus(consensusConfig, "TestUTXOCommitmentDAAStampRule")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		// A chain plus a side block, so that the tip's merge set holds more than just its selected
		// parent and the two rules have a chance to disagree on more than one entry.
		blockA, _, err := tc.AddBlock([]*externalapi.DomainHash{consensusConfig.GenesisHash}, nil, nil)
		if err != nil {
			t.Fatalf("AddBlock A: %+v", err)
		}
		blockB, _, err := tc.AddBlock([]*externalapi.DomainHash{blockA}, nil, nil)
		if err != nil {
			t.Fatalf("AddBlock B: %+v", err)
		}
		sideBlock, _, err := tc.AddBlock([]*externalapi.DomainHash{blockA}, nil, nil)
		if err != nil {
			t.Fatalf("AddBlock side: %+v", err)
		}
		merger, _, err := tc.AddBlock([]*externalapi.DomainHash{blockB, sideBlock}, nil, nil)
		if err != nil {
			t.Fatalf("AddBlock merger: %+v", err)
		}
		tip, _, err := tc.AddBlock([]*externalapi.DomainHash{merger}, nil, nil)
		if err != nil {
			t.Fatalf("AddBlock tip: %+v", err)
		}

		for _, blockHash := range []*externalapi.DomainHash{merger, tip} {
			mergingHash := commitmentUnderRule(t, tc, blockHash, mergingBlockRule)
			creatingHash := commitmentUnderRule(t, tc, blockHash, creatingBlockRule)

			header, err := tc.BlockHeaderStore().BlockHeader(tc.DatabaseContext(), model.NewStagingArea(), blockHash)
			if err != nil {
				t.Fatalf("BlockHeader: %+v", err)
			}
			t.Logf("block %s daaScore=%d\n  header (producer) : %s\n  merging-block rule: %s\n  creating-block rule: %s",
				blockHash, header.DAAScore(), header.UTXOCommitment(), mergingHash, creatingHash)

			if mergingHash.Equal(creatingHash) {
				t.Fatalf("block %s: the two DAA-stamp rules produced the SAME commitment, so this DAG "+
					"cannot discriminate between them - the fixture is wrong, not the code", blockHash)
			}

			if header.UTXOCommitment().Equal(creatingHash) {
				t.Fatalf("block %s: the commitment this node produced follows the CREATING-block DAA-stamp "+
					"rule (%s). Every block on mainnet was mined under the MERGING-block rule (%s), so a node "+
					"stamping this way computes a different commitment than the header of essentially every "+
					"block that exists and rejects the chain with ErrBadUTXOCommitment.",
					blockHash, creatingHash, mergingHash)
			}
			if !header.UTXOCommitment().Equal(mergingHash) {
				t.Fatalf("block %s: header commitment %s matches NEITHER DAA-stamp rule "+
					"(merging-block rule gives %s, creating-block rule gives %s) - something other than the "+
					"DAA stamp diverged between the producer and this replay",
					blockHash, header.UTXOCommitment(), mergingHash, creatingHash)
			}
		}
	})
}

// commitmentUnderRule replays the block's own stored acceptance data on top of its selected parent's
// stored multiset - exactly what calculateMultiset does - under the given DAA-stamp rule.
func commitmentUnderRule(t *testing.T, tc testapi.TestConsensus, blockHash *externalapi.DomainHash,
	rule daaStampRule) *externalapi.DomainHash {
	t.Helper()
	stagingArea := model.NewStagingArea()

	header, err := tc.BlockHeaderStore().BlockHeader(tc.DatabaseContext(), stagingArea, blockHash)
	if err != nil {
		t.Fatalf("BlockHeader(%s): %+v", blockHash, err)
	}
	ghostdagData, err := tc.GHOSTDAGDataStore().Get(tc.DatabaseContext(), stagingArea, blockHash, false)
	if err != nil {
		t.Fatalf("GHOSTDAGData(%s): %+v", blockHash, err)
	}
	acceptanceData, err := tc.AcceptanceDataStore().Get(tc.DatabaseContext(), stagingArea, blockHash)
	if err != nil {
		t.Fatalf("AcceptanceData(%s): %+v", blockHash, err)
	}
	ms, err := tc.MultisetStore().Get(tc.DatabaseContext(), stagingArea, ghostdagData.SelectedParent())
	if err != nil {
		t.Fatalf("Multiset(selectedParent of %s): %+v", blockHash, err)
	}
	ms = ms.Clone()

	for _, blockAcceptanceData := range acceptanceData {
		daaScore := header.DAAScore()
		if rule == creatingBlockRule {
			creatingHeader, err := tc.BlockHeaderStore().BlockHeader(
				tc.DatabaseContext(), stagingArea, blockAcceptanceData.BlockHash)
			if err != nil {
				t.Fatalf("BlockHeader(%s): %+v", blockAcceptanceData.BlockHash, err)
			}
			daaScore = creatingHeader.DAAScore()
		}
		for i, transactionAcceptanceData := range blockAcceptanceData.TransactionAcceptanceData {
			if !transactionAcceptanceData.IsAccepted {
				continue
			}
			addTransactionToMultisetForTest(t, ms, transactionAcceptanceData.Transaction, daaScore, i == 0)
		}
	}

	return ms.Hash()
}

func addTransactionToMultisetForTest(t *testing.T, ms model.Multiset,
	transaction *externalapi.DomainTransaction, daaScore uint64, isCoinbase bool) {
	t.Helper()
	transactionID := consensushashing.TransactionID(transaction)
	for _, input := range transaction.Inputs {
		serialized, err := utxo.SerializeUTXO(input.UTXOEntry, &input.PreviousOutpoint)
		if err != nil {
			t.Fatalf("SerializeUTXO: %+v", err)
		}
		ms.Remove(serialized)
	}
	for i, output := range transaction.Outputs {
		if i > math.MaxUint32 {
			t.Fatalf("output index overflow")
		}
		outpoint := &externalapi.DomainOutpoint{TransactionID: *transactionID, Index: uint32(i)}
		entry := utxo.NewUTXOEntry(output.Value, output.ScriptPublicKey, isCoinbase, daaScore)
		serialized, err := utxo.SerializeUTXO(entry, outpoint)
		if err != nil {
			t.Fatalf("SerializeUTXO: %+v", err)
		}
		ms.Add(serialized)
	}
}

// TestUTXOEntryDAAScoreIsStableAcrossDescendants pins the invariant that makes the merging-block
// stamp well defined: a coin is stamped exactly once, by the block that merged it, and resolving any
// later descendant's past UTXO does not re-stamp it. If the stamp were a function of the block doing
// the resolving, the same coin would serialize differently depending on who asked, and no two nodes
// resolving at different tips could agree on a UTXO commitment.
func TestUTXOEntryDAAScoreIsStableAcrossDescendants(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		consensusConfig.BlockCoinbaseMaturity = 0
		factory := consensus.NewFactory()
		tc, teardown, err := factory.NewTestConsensus(consensusConfig, "TestUTXOEntryDAAScoreIsStableAcrossDescendants")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		blockAHash, _, err := tc.AddBlock([]*externalapi.DomainHash{consensusConfig.GenesisHash}, nil, nil)
		if err != nil {
			t.Fatalf("AddBlock A: %+v", err)
		}
		// sideHash is NOT on the selected chain at the time it is created; its coinbase only enters
		// the UTXO set when mergerHash merges it.
		sideHash, _, err := tc.AddBlock([]*externalapi.DomainHash{blockAHash}, nil, nil)
		if err != nil {
			t.Fatalf("AddBlock side: %+v", err)
		}
		blockBHash, _, err := tc.AddBlock([]*externalapi.DomainHash{blockAHash}, nil, nil)
		if err != nil {
			t.Fatalf("AddBlock B: %+v", err)
		}
		mergerHash, _, err := tc.AddBlock([]*externalapi.DomainHash{blockBHash, sideHash}, nil, nil)
		if err != nil {
			t.Fatalf("AddBlock merger: %+v", err)
		}

		mergerHeader, err := tc.BlockHeaderStore().BlockHeader(tc.DatabaseContext(), model.NewStagingArea(), mergerHash)
		if err != nil {
			t.Fatalf("BlockHeader(merger): %+v", err)
		}

		sideBlock, _, err := tc.GetBlock(sideHash)
		if err != nil {
			t.Fatalf("GetBlock(side): %+v", err)
		}
		sideCoinbaseOutpoint := &externalapi.DomainOutpoint{
			TransactionID: *consensushashing.TransactionID(sideBlock.Transactions[0]),
			Index:         0,
		}

		// Whoever resolves it, the side block's coinbase must carry the DAA score of the block that
		// merged it - the merger - and never that of the block being resolved.
		assertEntryDAAScore(t, tc, mergerHash, sideCoinbaseOutpoint, mergerHeader.DAAScore())

		descendant := mergerHash
		for i := range 3 {
			descendant, _, err = tc.AddBlock([]*externalapi.DomainHash{descendant}, nil, nil)
			if err != nil {
				t.Fatalf("AddBlock descendant %d: %+v", i, err)
			}
			assertEntryDAAScore(t, tc, descendant, sideCoinbaseOutpoint, mergerHeader.DAAScore())
		}
	})
}

func assertEntryDAAScore(t *testing.T, tc testapi.TestConsensus, resolvedBlock *externalapi.DomainHash,
	outpoint *externalapi.DomainOutpoint, expectedDAAScore uint64) {
	t.Helper()

	iterator, err := tc.ConsensusStateManager().RestorePastUTXOSetIterator(model.NewStagingArea(), resolvedBlock)
	if err != nil {
		t.Fatalf("RestorePastUTXOSetIterator(%s): %+v", resolvedBlock, err)
	}
	defer iterator.Close()

	for ok := iterator.First(); ok; ok = iterator.Next() {
		currentOutpoint, entry, err := iterator.Get()
		if err != nil {
			t.Fatalf("iterator.Get: %+v", err)
		}
		if !currentOutpoint.Equal(outpoint) {
			continue
		}
		if entry.BlockDAAScore() != expectedDAAScore {
			t.Fatalf("resolving the past UTXO of %s stamped %s:%d with DAA score %d, want %d - the stamp "+
				"must be the merging block's, so it cannot change with who resolves it",
				resolvedBlock, &outpoint.TransactionID, outpoint.Index, entry.BlockDAAScore(), expectedDAAScore)
		}
		return
	}
	t.Fatalf("outpoint %s:%d not found in the past UTXO set of %s",
		&outpoint.TransactionID, outpoint.Index, resolvedBlock)
}
