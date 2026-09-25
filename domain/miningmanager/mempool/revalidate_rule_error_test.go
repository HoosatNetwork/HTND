package mempool

import (
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/ruleerrors"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/v2/domain/consensusreference"
	"github.com/pkg/errors"
)

// failingPopulateConsensus fails every attempt to populate a transaction with consensus data, as
// consensus does for a transaction that was valid when accepted but no longer is.
type failingPopulateConsensus struct {
	externalapi.Consensus
	err error
}

func (c failingPopulateConsensus) ValidateTransactionAndPopulateWithConsensusData(*externalapi.DomainTransaction) error {
	return c.err
}

// TestRevalidateRemovesTransactionFailingWithRuleError pins that a high-priority transaction which
// consensus now rejects is removed during revalidation, rather than aborting revalidation and staying
// in the pool with the inputs revalidation had already cleared.
func TestRevalidateRemovesTransactionFailingWithRuleError(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		tc, teardown, err := consensus.NewFactory().NewTestConsensus(consensusConfig, "TestRevalidateRemovesTransactionFailingWithRuleError")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		var tcAsConsensus externalapi.Consensus = tc
		tcAsConsensusPointer := &tcAsConsensus
		defer func() { tcAsConsensus = tc }()

		setUp := func() (*mempool, *externalapi.DomainTransactionID) {
			tcAsConsensus = tc
			mp := New(DefaultConfig(tc.DAGParams()), consensusreference.NewConsensusReference(&tcAsConsensusPointer)).(*mempool)
			funding := testutils.CreateTransactionWithOutput(100_000)
			if err := testutils.StageTransactionOutputsToVirtual(tc, funding, 0); err != nil {
				t.Fatalf("StageTransactionOutputsToVirtual: %+v", err)
			}
			transaction, err := testutils.CreateTransaction(funding, 1_000)
			if err != nil {
				t.Fatalf("CreateTransaction: %+v", err)
			}
			if _, err := mp.ValidateAndInsertTransaction(transaction, true, false, true); err != nil {
				t.Fatalf("ValidateAndInsertTransaction: %+v", err)
			}
			return mp, consensushashing.TransactionID(transaction)
		}

		t.Run("rule error removes the transaction", func(t *testing.T) {
			mp, transactionID := setUp()
			tcAsConsensus = failingPopulateConsensus{Consensus: tc,
				err: errors.Wrapf(ruleerrors.ErrUnfinalizedTx, "no longer final")}

			valid, err := mp.RevalidateHighPriorityTransactions()
			if err != nil {
				t.Fatalf("revalidation should not fail on a rule error, got: %+v", err)
			}
			if len(valid) != 0 {
				t.Fatalf("expected no valid transactions, got %d", len(valid))
			}
			if _, ok := mp.transactionsPool.allTransactions[*transactionID]; ok {
				t.Fatalf("transaction rejected by consensus should have been removed from the mempool")
			}
		})

		t.Run("non-rule error is still returned", func(t *testing.T) {
			mp, _ := setUp()
			tcAsConsensus = failingPopulateConsensus{Consensus: tc, err: errors.New("database is unavailable")}

			if _, err := mp.RevalidateHighPriorityTransactions(); err == nil {
				t.Fatalf("expected a non-rule error to be returned")
			}
		})
	})
}
