package mempool

import (
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/v2/domain/consensusreference"
)

// TestOrphanChainIsPromotedTransitively checks that accepting a transaction promotes not just the
// orphans spending it, but also the orphans spending those, all the way down the chain.
func TestOrphanChainIsPromotedTransitively(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		tc, teardown, err := consensus.NewFactory().NewTestConsensus(consensusConfig, "TestOrphanChainIsPromotedTransitively")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		tcAsConsensus := tc.(externalapi.Consensus)
		tcAsConsensusPointer := &tcAsConsensus
		mp := New(DefaultConfig(tc.DAGParams()), consensusreference.NewConsensusReference(&tcAsConsensusPointer)).(*mempool)

		funding := testutils.CreateTransactionWithOutput(100_000)
		if err := testutils.StageTransactionOutputsToVirtual(tc, funding, 0); err != nil {
			t.Fatalf("StageTransactionOutputsToVirtual: %+v", err)
		}

		const chainLength = 4
		chain := make([]*externalapi.DomainTransaction, chainLength)
		previous := funding
		for i := range chain {
			chain[i], err = testutils.CreateTransaction(previous, 1_000)
			if err != nil {
				t.Fatalf("CreateTransaction(%d): %+v", i, err)
			}
			previous = chain[i]
		}

		// Each output has to be in virtual, the UTXO set of UTXO-valid blocks, before the
		// transaction that spends it can leave the orphan pool.
		for i := 0; i < chainLength-1; i++ {
			if err := testutils.StageCreatedOutputsToVirtual(tc, chain[i], 0); err != nil {
				t.Fatalf("StageCreatedOutputsToVirtual(%d): %+v", i, err)
			}
		}

		// Insert everything but the head, deepest first, so each one is an orphan when it arrives.
		for i := chainLength - 1; i >= 1; i-- {
			accepted, err := mp.ValidateAndInsertTransaction(chain[i], false, true, false)
			if err != nil {
				t.Fatalf("ValidateAndInsertTransaction(%d): %+v", i, err)
			}
			if len(accepted) != 0 {
				t.Fatalf("expected transaction %d to be an orphan, got %d accepted", i, len(accepted))
			}
		}

		accepted, err := mp.ValidateAndInsertTransaction(chain[0], false, true, false)
		if err != nil {
			t.Fatalf("ValidateAndInsertTransaction(head): %+v", err)
		}
		if len(accepted) != chainLength {
			t.Fatalf("expected all %d transactions accepted, got %d (orphans left: %d)",
				chainLength, len(accepted), mp.orphansPool.orphanTransactionCount())
		}
		if mp.orphansPool.orphanTransactionCount() != 0 {
			t.Fatalf("expected no orphans left, got %d", mp.orphansPool.orphanTransactionCount())
		}
		if mp.transactionsPool.transactionCount() != chainLength {
			t.Fatalf("expected %d transactions in the pool, got %d", chainLength, mp.transactionsPool.transactionCount())
		}
	})
}
