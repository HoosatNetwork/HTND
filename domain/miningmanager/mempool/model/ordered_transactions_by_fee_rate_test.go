package model

import (
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
)

// feeRateTestTransaction builds a MempoolTransaction with a distinct ID (via its payload) and a fee
// and mass populated, since findTransactionIndex refuses to order a transaction that lacks either.
func feeRateTestTransaction(t *testing.T, payloadByte byte, fee, mass uint64) *MempoolTransaction {
	t.Helper()
	tx := &externalapi.DomainTransaction{Payload: []byte{payloadByte}}
	tx.StoreFee(fee)
	tx.StoreMass(mass)
	return NewMempoolTransaction(tx, IDToTransactionMap{}, false, 0)
}

// TestGetByIndexOutOfBoundsReturnsNil pins that GetByIndex reports an out-of-range index the same
// way RemoveAtIndex always has - by returning cleanly - instead of panicking. Real production traffic
// hits this: transactions_pool.go's removeTransaction tolerates Remove not finding an entry here
// ("This should never happen but sometimes does"), and before this fix, limitTransactionCount's
// eviction loop indexed past the resulting shorter slice with no bounds check at all.
func TestGetByIndexOutOfBoundsReturnsNil(t *testing.T) {
	var tobf TransactionsOrderedByFeeRate

	if got := tobf.GetByIndex(0); got != nil {
		t.Errorf("GetByIndex(0) on an empty set: expected nil, got %v", got)
	}
	if got := tobf.GetByIndex(5); got != nil {
		t.Errorf("GetByIndex(5) on an empty set: expected nil, got %v", got)
	}
	if got := tobf.GetByIndex(-1); got != nil {
		t.Errorf("GetByIndex(-1): expected nil, got %v", got)
	}

	txA := feeRateTestTransaction(t, 1, 100, 10)
	txB := feeRateTestTransaction(t, 2, 200, 10)
	if err := tobf.Push(txA); err != nil {
		t.Fatalf("Push txA: %+v", err)
	}
	if err := tobf.Push(txB); err != nil {
		t.Fatalf("Push txB: %+v", err)
	}

	if got := tobf.GetByIndex(0); got == nil {
		t.Error("GetByIndex(0) on a 2-element set: expected a transaction, got nil")
	}
	if got := tobf.GetByIndex(1); got == nil {
		t.Error("GetByIndex(1) on a 2-element set: expected a transaction, got nil")
	}
	// The exact index that used to panic: equal to the slice length.
	if got := tobf.GetByIndex(2); got != nil {
		t.Errorf("GetByIndex(2) on a 2-element set (index == len): expected nil, got %v", got)
	}
	if got := tobf.GetByIndex(100); got != nil {
		t.Errorf("GetByIndex(100) on a 2-element set: expected nil, got %v", got)
	}
}
