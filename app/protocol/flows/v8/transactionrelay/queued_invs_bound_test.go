package transactionrelay

import (
	"testing"

	"github.com/HoosatNetwork/HTND/app/appmessage"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/infrastructure/network/netadapter/router"
)

func queuedInvTransactionIDs(flow *handleRelayedTransactionsFlow) int {
	count := 0
	for _, inv := range flow.invsQueue {
		count += len(inv.TxIDs)
	}
	return count
}

// TestInvsQueuedWhileAwaitingTransactionsAreBounded pins that inv messages arriving while the flow waits for a
// requested transaction are queued only up to a bound. Every such inv used to be kept, and a peer that answered
// slowly while sending invs continuously grew the queue - each inv up to MaxInvPerTxInvMsg IDs - until the node ran
// out of memory. Invs past the bound are dropped, as invs already are when the route is full.
func TestInvsQueuedWhileAwaitingTransactionsAreBounded(t *testing.T) {
	const maxQueuedIDs = 4 * appmessage.MaxInvPerTxInvMsg

	flow := newTestFlow()
	flow.incomingRoute = router.NewRouteWithCapacity("incoming", 100)

	_, transactionID := testTransaction(1)
	ids := make([]*externalapi.DomainTransactionID, appmessage.MaxInvPerTxInvMsg)
	for i := range ids {
		ids[i] = transactionID
	}
	const invCount = 7
	for range invCount {
		if err := flow.incomingRoute.Enqueue(appmessage.NewMsgInvTransaction(ids)); err != nil {
			t.Fatalf("Enqueue inv: %+v", err)
		}
	}
	transaction, _ := testTransaction(2)
	if err := flow.incomingRoute.Enqueue(appmessage.DomainTransactionToMsgTx(transaction)); err != nil {
		t.Fatalf("Enqueue tx: %+v", err)
	}

	msgTx, msgNotFound, err := flow.readMsgTxOrNotFound()
	if err != nil || msgTx == nil || msgNotFound != nil {
		t.Fatalf("expected the transaction after the invs, got tx %v, not found %v, err %+v", msgTx, msgNotFound, err)
	}
	if queued := queuedInvTransactionIDs(flow); queued > maxQueuedIDs {
		t.Fatalf("queued %d inv transaction IDs while waiting, want at most %d", queued, maxQueuedIDs)
	}
	if len(flow.invsQueue) == 0 {
		t.Fatalf("invs within the bound must still be queued")
	}
}
