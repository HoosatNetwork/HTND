package mempool

import (
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/subnetworks"
	"github.com/HoosatNetwork/HTND/domain/miningmanager/mempool/model"
)

// TestGetRedeemersDiamond builds a chain of diamonds - A spends into B and C, D spends both, D
// spends into E and F, G spends both, and so on - and checks that getRedeemers returns every
// descendant exactly once. Without deduplication each diamond doubles the number of paths to the
// transactions below it, so the walk is exponential in the chain length.
func TestGetRedeemersDiamond(t *testing.T) {
	const diamonds = 40

	nextLockTime := uint64(0)
	newTransaction := func(parents ...*model.MempoolTransaction) *model.MempoolTransaction {
		nextLockTime++
		parentsInPool := model.IDToTransactionMap{}
		for _, parent := range parents {
			parentsInPool[*parent.TransactionID()] = parent
		}
		return model.NewMempoolTransaction(&externalapi.DomainTransaction{
			LockTime:     nextLockTime,
			SubnetworkID: subnetworks.SubnetworkIDNative,
		}, parentsInPool, false, 0)
	}

	tp := &transactionsPool{chainedTransactionsByParentID: model.IDToTransactionsSliceMap{}}
	link := func(transaction *model.MempoolTransaction) {
		for parentID := range transaction.ParentTransactionsInPool() {
			tp.chainedTransactionsByParentID[parentID] = append(tp.chainedTransactionsByParentID[parentID], transaction)
		}
	}

	root := newTransaction()
	top := root
	expected := 0
	for i := 0; i < diamonds; i++ {
		left := newTransaction(top)
		right := newTransaction(top)
		bottom := newTransaction(left, right)
		link(left)
		link(right)
		link(bottom)
		expected += 3
		top = bottom
	}

	done := make(chan []*model.MempoolTransaction, 1)
	go func() { done <- tp.getRedeemers(root) }()

	select {
	case redeemers := <-done:
		if len(redeemers) != expected {
			t.Fatalf("expected %d redeemers, got %d", expected, len(redeemers))
		}
		seen := make(map[externalapi.DomainTransactionID]struct{}, len(redeemers))
		for _, redeemer := range redeemers {
			id := *redeemer.TransactionID()
			if _, ok := seen[id]; ok {
				t.Fatalf("redeemer %s returned more than once", id)
			}
			seen[id] = struct{}{}
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("getRedeemers did not finish on %d chained diamonds", diamonds)
	}
}
