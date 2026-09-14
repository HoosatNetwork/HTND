package flowcontext

import (
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/infrastructure/network/netadapter"
)

func newPropagationTestFlowContext() *FlowContext {
	f := New(nil, nil, nil, &netadapter.NetAdapter{}, nil)
	// No rebroadcast is due, so a block brings nothing new to propagate.
	f.lastRebroadcastTime = time.Now()
	return f
}

// TestBlockFlushesHeldTransactionIDs pins that a new block sends transaction IDs that were held back
// because they arrived within TransactionIDPropagationInterval of the previous propagation. A block that
// brought nothing new used to return before reaching the propagation queue, so on a quiet network such
// an ID was not relayed to any peer until another transaction happened to be enqueued.
func TestBlockFlushesHeldTransactionIDs(t *testing.T) {
	f := newPropagationTestFlowContext()
	heldID := externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{1})
	f.transactionIDsToPropagate = []*externalapi.DomainTransactionID{heldID}
	f.lastTransactionIDPropagationTime = time.Now().Add(-time.Hour)

	if err := f.broadcastTransactionsAfterBlockAdded(nil, nil); err != nil {
		t.Fatalf("broadcastTransactionsAfterBlockAdded: %+v", err)
	}
	if len(f.transactionIDsToPropagate) != 0 {
		t.Fatalf("expected the held transaction ID to be propagated, %d still queued", len(f.transactionIDsToPropagate))
	}
}

// TestBlockDoesNotRestartPropagationIntervalWhenNothingIsQueued pins that flushing an empty queue on every
// block does not count as a propagation. Restarting the interval there would hold back the next new
// transaction for up to a block, even though nothing was sent.
func TestBlockDoesNotRestartPropagationIntervalWhenNothingIsQueued(t *testing.T) {
	f := newPropagationTestFlowContext()
	lastPropagation := time.Now().Add(-time.Hour)
	f.lastTransactionIDPropagationTime = lastPropagation

	if err := f.broadcastTransactionsAfterBlockAdded(nil, nil); err != nil {
		t.Fatalf("broadcastTransactionsAfterBlockAdded: %+v", err)
	}
	if !f.lastTransactionIDPropagationTime.Equal(lastPropagation) {
		t.Fatalf("an empty flush moved the last propagation time from %s to %s",
			lastPropagation, f.lastTransactionIDPropagationTime)
	}
}

// TestTransactionIDsWithinIntervalStayBatched pins that IDs arriving within the propagation interval are
// still batched rather than sent one inv at a time.
func TestTransactionIDsWithinIntervalStayBatched(t *testing.T) {
	f := newPropagationTestFlowContext()
	f.lastTransactionIDPropagationTime = time.Now()
	heldID := externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{2})

	if err := f.EnqueueTransactionIDsForPropagation([]*externalapi.DomainTransactionID{heldID}); err != nil {
		t.Fatalf("EnqueueTransactionIDsForPropagation: %+v", err)
	}
	if len(f.transactionIDsToPropagate) != 1 {
		t.Fatalf("expected the ID to be held for batching, %d queued", len(f.transactionIDsToPropagate))
	}
}
