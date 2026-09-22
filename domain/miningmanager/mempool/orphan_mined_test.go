package mempool

import (
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/domain/consensusreference"
)

// TestMinedOrphanPromotesItsOrphanChild checks that when a block includes a transaction this node
// only had as an orphan, the orphans spending it are promoted into the mempool rather than deleted.
func TestMinedOrphanPromotesItsOrphanChild(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		tc, teardown, err := consensus.NewFactory().NewTestConsensus(consensusConfig, "TestMinedOrphanPromotesItsOrphanChild")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		tcAsConsensus := tc.(externalapi.Consensus)
		tcAsConsensusPointer := &tcAsConsensus
		mp := New(DefaultConfig(tc.DAGParams()), consensusreference.NewConsensusReference(&tcAsConsensusPointer)).(*mempool)

		// grandparent is unknown to both consensus and the mempool, so parent and child are orphans.
		grandparent := testutils.CreateTransactionWithOutput(100_000)
		parent, err := testutils.CreateTransaction(grandparent, 1_000)
		if err != nil {
			t.Fatalf("CreateTransaction(parent): %+v", err)
		}
		child, err := testutils.CreateTransaction(parent, 1_000)
		if err != nil {
			t.Fatalf("CreateTransaction(child): %+v", err)
		}
		for _, transaction := range []*externalapi.DomainTransaction{parent, child} {
			if _, err := mp.ValidateAndInsertTransaction(transaction, false, true, false); err != nil {
				t.Fatalf("ValidateAndInsertTransaction: %+v", err)
			}
		}
		if mp.orphansPool.orphanTransactionCount() != 2 {
			t.Fatalf("expected 2 orphans, got %d", mp.orphansPool.orphanTransactionCount())
		}

		// A UTXO-valid block has accepted parent, so its outputs are in virtual. Only then can
		// the child be promoted.
		if err := testutils.StageCreatedOutputsToVirtual(tc, parent, 0); err != nil {
			t.Fatalf("StageCreatedOutputsToVirtual(parent): %+v", err)
		}
		coinbase := testutils.CreateTransactionWithOutput(1)
		accepted, err := mp.HandleNewBlockTransactions([]*externalapi.DomainTransaction{coinbase, parent})
		if err != nil {
			t.Fatalf("HandleNewBlockTransactions: %+v", err)
		}

		childID := consensushashing.TransactionID(child)
		if _, ok := mp.transactionsPool.allTransactions[*childID]; !ok {
			t.Fatalf("child of the mined orphan is not in the mempool (orphans left: %d, accepted: %d)",
				mp.orphansPool.orphanTransactionCount(), len(accepted))
		}
		if len(accepted) != 1 || !consensushashing.TransactionID(accepted[0]).Equal(childID) {
			t.Fatalf("expected the child to be reported as accepted, got %d transactions", len(accepted))
		}
		if mp.orphansPool.orphanTransactionCount() != 0 {
			t.Fatalf("expected no orphans left, got %d", mp.orphansPool.orphanTransactionCount())
		}
	})
}
