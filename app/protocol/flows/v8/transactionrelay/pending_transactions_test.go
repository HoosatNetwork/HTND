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

// bulkyTestTransaction builds a transaction whose payload is the given shared slice, made unique by
// its lock time, so a test can hold many large transactions without allocating each payload.
func bulkyTestTransaction(n int, payload []byte) (*externalapi.DomainTransaction, *externalapi.DomainTransactionID) {
	transaction := &externalapi.DomainTransaction{
		Inputs:   []*externalapi.DomainTransactionInput{},
		Outputs:  []*externalapi.DomainTransactionOutput{},
		LockTime: uint64(n),
		Payload:  payload,
	}
	return transaction, consensushashing.TransactionID(transaction)
}

func heldPayloadBytes(flow *handleRelayedTransactionsFlow) int {
	total := 0
	for _, transaction := range flow.pendingTransactions {
		total += len(transaction.Payload)
	}
	return total
}

// TestHoldTransactionIsBoundedByBytes pins that the held buffer is bounded by size, not only by
// count: a peer serving large transactions while this node syncs must not be able to grow it without
// limit. Overflow still drops the oldest.
func TestHoldTransactionIsBoundedByBytes(t *testing.T) {
	const ceiling = 64 << 20
	flow := newTestFlow()
	payload := make([]byte, 1<<20)

	first, firstID := bulkyTestTransaction(0, payload)
	flow.holdTransaction(first, firstID)
	var lastID *externalapi.DomainTransactionID
	for i := 1; i < 1024; i++ {
		transaction, transactionID := bulkyTestTransaction(i, payload)
		flow.holdTransaction(transaction, transactionID)
		lastID = transactionID
	}

	if held := heldPayloadBytes(flow); held > ceiling {
		t.Fatalf("held %d payload bytes, more than %d", held, ceiling)
	}
	if flow.isKnownTransactionHeld(firstID) {
		t.Fatalf("the oldest held transaction should have been evicted")
	}
	if !flow.isKnownTransactionHeld(lastID) {
		t.Fatalf("the newest held transaction should have been kept")
	}
	if len(flow.pendingTransactionIDs) != len(flow.pendingTransactions) {
		t.Fatalf("index and buffer disagree: %d ids, %d transactions",
			len(flow.pendingTransactionIDs), len(flow.pendingTransactions))
	}
}

// TestHoldTransactionDropsOversized pins that a single transaction too large for the whole budget is
// not held at all, rather than evicting everything else to make room for it.
func TestHoldTransactionDropsOversized(t *testing.T) {
	flow := newTestFlow()
	small, smallID := testTransaction(1)
	flow.holdTransaction(small, smallID)

	huge, hugeID := bulkyTestTransaction(2, make([]byte, 65<<20))
	flow.holdTransaction(huge, hugeID)

	if flow.isKnownTransactionHeld(hugeID) {
		t.Fatalf("an oversized transaction should not be held")
	}
	if !flow.isKnownTransactionHeld(smallID) {
		t.Fatalf("holding an oversized transaction should not evict what is already held")
	}
}
