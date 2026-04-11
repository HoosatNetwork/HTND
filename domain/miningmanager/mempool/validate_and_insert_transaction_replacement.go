package mempool

import (
	"fmt"
	"sort"

	"github.com/Hoosat-Oy/HTND/infrastructure/logger"

	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/consensushashing"
	"github.com/Hoosat-Oy/HTND/domain/miningmanager/mempool/model"
)

func (mp *mempool) validateAndInsertTransactionReplacement(transaction *externalapi.DomainTransaction, isHighPriority bool) (
	acceptedTransactions []*externalapi.DomainTransaction,
	replacedTransaction *externalapi.DomainTransaction,
	err error,
) {
	onEnd := logger.LogAndMeasureExecutionTime(log,
		fmt.Sprintf("validateAndInsertTransactionReplacement %s", consensushashing.TransactionID(transaction)))
	defer onEnd()

	// Populate mass in the beginning, it will be used in multiple places throughout the validation and insertion.
	mp.consensusReference.Consensus().PopulateMass(transaction)

	// Validate in isolation (but do not reject double-spends here; those are handled by replacement policy).
	err = mp.validateTransactionInIsolation(transaction)
	if err != nil {
		return nil, nil, err
	}

	conflicts := mp.mempoolConflicts(transaction)
	if len(conflicts) == 0 {
		acceptedTransactions, err := mp.validateAndInsertTransaction(transaction, isHighPriority, false)
		return acceptedTransactions, nil, err
	}

	parentsInPool, missingOutpoints, err := mp.fillInputsAndGetMissingParents(transaction)
	if err != nil {
		return nil, nil, err
	}
	if len(missingOutpoints) > 0 {
		str := fmt.Sprintf("Transaction %s is an orphan, but replacement submission does not allow orphans",
			consensushashing.TransactionID(transaction))
		return nil, nil, transactionRuleError(RejectBadOrphan, str)
	}

	err = mp.validateTransactionInContext(transaction)
	if err != nil {
		return nil, nil, err
	}

	totalRemovedFee, totalRemovedMass := mp.replacementRemovalTotals(conflicts)

	// Replacement policy: new transaction must pay more (and at a higher fee rate) than the transactions it evicts.
	if transaction.Fee <= totalRemovedFee {
		str := fmt.Sprintf("replacement transaction %s fee (%d) is not higher than evicted transactions fee (%d)",
			consensushashing.TransactionID(transaction), transaction.Fee, totalRemovedFee)
		return nil, nil, transactionRuleError(RejectInsufficientFee, str)
	}
	if transaction.Mass == 0 || totalRemovedMass == 0 {
		return nil, nil, transactionRuleError(RejectInvalid, "replacement fee-rate calculation expects populated mass")
	}

	replacementFeeRate := float64(transaction.Fee) / float64(transaction.Mass)
	removedFeeRate := float64(totalRemovedFee) / float64(totalRemovedMass)
	if replacementFeeRate <= removedFeeRate {
		str := fmt.Sprintf("replacement transaction %s fee rate (%.8f) is not higher than evicted transactions fee rate (%.8f)",
			consensushashing.TransactionID(transaction), replacementFeeRate, removedFeeRate)
		return nil, nil, transactionRuleError(RejectInsufficientFee, str)
	}

	// Capture one of the directly-conflicted transactions for RPC response before removal.
	replacedTransaction = conflicts[0].Transaction().Clone()

	// Remove conflicts (and their redeemers) from the mempool.
	for _, conflict := range conflicts {
		err := mp.removeTransaction(conflict.TransactionID(), true)
		if err != nil {
			return nil, nil, err
		}
	}

	// Recompute parents-in-pool after removals to avoid stale references.
	parentsInPool = mp.transactionsPool.getParentTransactionsInPool(transaction)

	mempoolTransaction, err := mp.transactionsPool.addTransaction(transaction, parentsInPool, isHighPriority)
	if err != nil {
		// Insertion failed after eviction; attempt to avoid leaving stale UTXO entries in the tx.
		return nil, nil, err
	}

	// Record the transaction for compound transaction rate limiting.
	txID := consensushashing.TransactionID(transaction)
	mp.compoundTxRateLimiter.recordTransaction(transaction, txID.String())

	acceptedOrphans, err := mp.orphansPool.processOrphansAfterAcceptedTransaction(mempoolTransaction.Transaction())
	if err != nil {
		return nil, nil, err
	}

	acceptedTransactions = append([]*externalapi.DomainTransaction{transaction.Clone()}, acceptedOrphans...) // these pointers leave the mempool, hence we clone.

	err = mp.transactionsPool.limitTransactionCount()
	if err != nil {
		return nil, nil, err
	}

	return acceptedTransactions, replacedTransaction, nil
}

func (mp *mempool) mempoolConflicts(transaction *externalapi.DomainTransaction) []*model.MempoolTransaction {
	conflictMap := make(map[externalapi.DomainTransactionID]*model.MempoolTransaction)
	for _, input := range transaction.Inputs {
		if existing, exists := mp.mempoolUTXOSet.transactionByPreviousOutpoint[input.PreviousOutpoint]; exists {
			conflictMap[*existing.TransactionID()] = existing
		}
	}

	conflicts := make([]*model.MempoolTransaction, 0, len(conflictMap))
	for _, tx := range conflictMap {
		conflicts = append(conflicts, tx)
	}

	sort.Slice(conflicts, func(i, j int) bool {
		// Ascending order for determinism.
		return conflicts[i].TransactionID().LessOrEqual(conflicts[j].TransactionID())
	})

	return conflicts
}

func (mp *mempool) replacementRemovalTotals(conflicts []*model.MempoolTransaction) (totalRemovedFee uint64, totalRemovedMass uint64) {
	transactionsToRemove := make(map[externalapi.DomainTransactionID]*model.MempoolTransaction)

	add := func(tx *model.MempoolTransaction) {
		id := *tx.TransactionID()
		if _, ok := transactionsToRemove[id]; ok {
			return
		}
		transactionsToRemove[id] = tx
		totalRemovedFee += tx.Transaction().Fee
		totalRemovedMass += tx.Transaction().Mass
	}

	for _, conflict := range conflicts {
		add(conflict)
		for _, redeemer := range mp.transactionsPool.getRedeemers(conflict) {
			add(redeemer)
		}
	}

	return totalRemovedFee, totalRemovedMass
}
