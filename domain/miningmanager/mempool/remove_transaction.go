package mempool

import (
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/miningmanager/mempool/model"
)

func (mp *mempool) removeTransaction(transactionID *externalapi.DomainTransactionID, removeRedeemers bool) error {
	if _, ok := mp.orphansPool.allOrphans[*transactionID]; ok {
		// Honour removeRedeemers here too: a mined orphan is removed with false, and its orphan
		// children must survive so processOrphansAfterAcceptedTransaction can promote them.
		return mp.orphansPool.removeOrphan(transactionID, removeRedeemers)
	}

	mempoolTransaction, ok := mp.transactionsPool.allTransactions[*transactionID]
	if !ok {
		return nil
	}

	transactionsToRemove := []*model.MempoolTransaction{mempoolTransaction}
	redeemers := mp.transactionsPool.getRedeemers(mempoolTransaction)
	if removeRedeemers {
		transactionsToRemove = append(transactionsToRemove, redeemers...)
	} else {
		for _, redeemer := range redeemers {
			redeemer.RemoveParentTransactionInPool(transactionID)
		}
	}

	for _, transactionToRemove := range transactionsToRemove {
		err := mp.removeTransactionFromSets(transactionToRemove, removeRedeemers)
		if err != nil {
			return err
		}
	}

	if removeRedeemers {
		err := mp.orphansPool.removeRedeemersOf(mempoolTransaction)
		if err != nil {
			return err
		}
	}

	return nil
}

func (mp *mempool) removeTransactionFromSets(mempoolTransaction *model.MempoolTransaction, removeRedeemers bool) error {
	mp.mempoolUTXOSet.removeTransaction(mempoolTransaction)

	err := mp.transactionsPool.removeTransaction(mempoolTransaction)
	if err != nil {
		return err
	}

	err = mp.orphansPool.updateOrphansAfterTransactionRemoved(mempoolTransaction, removeRedeemers)
	if err != nil {
		return err
	}

	return nil
}
