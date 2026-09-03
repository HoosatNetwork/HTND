package transactionrelay

import (
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
)

func testTransaction(n int) (*externalapi.DomainTransaction, *externalapi.DomainTransactionID) {
	transaction := &externalapi.DomainTransaction{
		Version: 0,
		Inputs:  []*externalapi.DomainTransactionInput{},
		Outputs: []*externalapi.DomainTransactionOutput{},
		Payload: []byte{byte(n), byte(n >> 8), byte(n >> 16), byte(n >> 24)},
	}
	return transaction, consensushashing.TransactionID(transaction)
}

func newTestFlow() *handleRelayedTransactionsFlow {
	return &handleRelayedTransactionsFlow{
		pendingTransactionIDs: make(map[externalapi.DomainTransactionID]struct{}),
	}
}

// TestHoldTransactionDeduplicates pins that re-holding the same transaction is a no-op, so a repeated
// inv for something already fetched and waiting cannot grow the buffer.
func TestHoldTransactionDeduplicates(t *testing.T) {
	flow := newTestFlow()
	transaction, transactionID := testTransaction(1)

	flow.holdTransaction(transaction, transactionID)
	flow.holdTransaction(transaction, transactionID)

	if got := len(flow.pendingTransactions); got != 1 {
		t.Fatalf("expected the duplicate to be ignored, got %d held transactions", got)
	}
	if !flow.isKnownTransactionHeld(transactionID) {
		t.Fatalf("a held transaction should be reported as already known")
	}
}

// isKnownTransactionHeld isolates the held-transaction half of isKnownTransaction, which otherwise
// needs a Domain to consult the mempool.
func (flow *handleRelayedTransactionsFlow) isKnownTransactionHeld(txID *externalapi.DomainTransactionID) bool {
	_, held := flow.pendingTransactionIDs[*txID]
	return held
}

// TestHoldTransactionEvictsOldestAtCapacity pins that the buffer is bounded and that overflow drops
// the OLDEST entry. A long IBD can outlast a great many relayed transactions, and the newest are the
// likeliest to still be valid by the time the node catches up.
func TestHoldTransactionEvictsOldestAtCapacity(t *testing.T) {
	flow := newTestFlow()

	first, firstID := testTransaction(0)
	flow.holdTransaction(first, firstID)
	for i := 1; i < maxPendingRelayedTransactions; i++ {
		transaction, transactionID := testTransaction(i)
		flow.holdTransaction(transaction, transactionID)
	}
	if got := len(flow.pendingTransactions); got != maxPendingRelayedTransactions {
		t.Fatalf("expected the buffer to fill to %d, got %d", maxPendingRelayedTransactions, got)
	}

	overflow, overflowID := testTransaction(maxPendingRelayedTransactions)
	flow.holdTransaction(overflow, overflowID)

	if got := len(flow.pendingTransactions); got != maxPendingRelayedTransactions {
		t.Fatalf("buffer exceeded its cap: got %d, want %d", got, maxPendingRelayedTransactions)
	}
	if flow.isKnownTransactionHeld(firstID) {
		t.Fatalf("the oldest held transaction should have been evicted")
	}
	if !flow.isKnownTransactionHeld(overflowID) {
		t.Fatalf("the newest held transaction should have been kept")
	}
}
