package transactionvalidator_test

import (
	"sync"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/testutils"
)

// TestPopulateFeeConcurrentReads guards the transaction fee against data races. In-context validation
// writes the fee while other goroutines may read the same transaction - RPC handlers read mempool
// transactions after the mempool lock is released. Run it with -race for it to mean anything.
func TestPopulateFeeConcurrentReads(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		tc, teardown, err := consensus.NewFactory().NewTestConsensus(consensusConfig, "TestPopulateFeeConcurrentReads")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		funding := testutils.CreateTransactionWithOutput(100_000)
		if err := testutils.StageTransactionOutputsToVirtual(tc, funding, 0); err != nil {
			t.Fatalf("StageTransactionOutputsToVirtual: %+v", err)
		}
		const fee = 1_000
		tx, err := testutils.CreateTransaction(funding, fee)
		if err != nil {
			t.Fatalf("CreateTransaction: %+v", err)
		}
		// Populate once up front so the inputs are filled before readers start; later calls only
		// rewrite the fee (and mass) on the shared transaction.
		if err := tc.ValidateTransactionAndPopulateWithConsensusData(tx); err != nil {
			t.Fatalf("ValidateTransactionAndPopulateWithConsensusData: %+v", err)
		}

		var wg sync.WaitGroup
		wg.Go(func() {
			for range 20 {
				_ = tc.ValidateTransactionAndPopulateWithConsensusData(tx)
			}
		})
		wg.Go(func() {
			for range 2000 {
				_ = tx.LoadFee()
			}
		})
		wg.Wait()

		if got := tx.LoadFee(); got != fee {
			t.Fatalf("populated fee is %d, want %d", got, fee)
		}
	})
}
