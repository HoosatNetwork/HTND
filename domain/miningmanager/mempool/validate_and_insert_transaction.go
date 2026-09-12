package mempool

import (
	"fmt"

	"github.com/HoosatNetwork/HTND/infrastructure/logger"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
)

func (mp *mempool) validateAndInsertTransaction(transaction *externalapi.DomainTransaction, isHighPriority bool,
	allowOrphan bool, isLocalSubmission bool,
) (acceptedTransactions []*externalapi.DomainTransaction, err error) {
	onEnd := logger.LogAndMeasureExecutionTime(log,
		fmt.Sprintf("validateAndInsertTransaction %s", consensushashing.TransactionID(transaction)))
	defer onEnd()

	// Populate mass in the beginning, it will be used in multiple places throughout the validation and insertion.
	mp.consensusReference.Consensus().PopulateMass(transaction)

	// Mass is known from here on, so the compound shape can be judged. This must also precede the
	// orphan branch below, so that a compound transaction arriving before its parents is protected
	// while it waits in the orphan pool.
	isHighPriority = mp.raisePriorityIfCompound(transaction, isHighPriority)

	err = mp.validateTransactionPreUTXOEntry(transaction)
	if err != nil {
		return nil, err
	}

	parentsInPool, missingOutpoints, err := mp.fillInputsAndGetMissingParents(transaction)
	if err != nil {
		return nil, err
	}

	if len(missingOutpoints) > 0 {
		if !allowOrphan {
			str := fmt.Sprintf("Transaction %s is an orphan, where allowOrphan = false",
				consensushashing.TransactionID(transaction))
			return nil, transactionRuleError(RejectBadOrphan, str)
		}

		return nil, mp.orphansPool.maybeAddOrphan(transaction, isHighPriority, isLocalSubmission)
	}

	err = mp.validateTransactionInContext(transaction, isLocalSubmission)
	if err != nil {
		return nil, err
	}

	mempoolTransaction, err := mp.transactionsPool.addTransaction(transaction, parentsInPool, isHighPriority)
	if err != nil {
		return nil, err
	}

	// Record the transaction against the compound rate limit - only when this node's own RPC
	// submitted it. Counting relayed traffic here is what let one busy address exhaust every node's
	// budget at once; see validateTransactionInContext.
	if isLocalSubmission {
		txID := consensushashing.TransactionID(transaction)
		mp.compoundTxRateLimiter.recordTransaction(transaction, txID.String())
	}

	acceptedOrphans, err := mp.orphansPool.processOrphansAfterAcceptedTransaction(mempoolTransaction.Transaction())
	if err != nil {
		return nil, err
	}

	// Accepted orphans are recorded at their original arrival time inside the orphan pool

	acceptedTransactions = append([]*externalapi.DomainTransaction{transaction.Clone()}, acceptedOrphans...) // these pointer leave the mempool, hence we clone.

	err = mp.transactionsPool.limitTransactionCount()
	if err != nil {
		return nil, err
	}

	return acceptedTransactions, nil
}

// raisePriorityIfCompound treats a compound transaction as high priority.
//
// High priority means two things in this mempool: expireOldTransactions leaves the transaction alone,
// and limitTransactionCount will not evict it to make room. A compound transaction needs both. It folds
// many coins into one, so it is large and slow to be selected into a block, and its inputs are exactly
// the coins the sender needs freed before anything else can be spent - if it expires unmined, the
// sender is back where it started and its dependants leave the pool with it.
//
// The node that receives the transaction over RPC already gives it priority; relay does not. So the
// same transaction was kept on the submitter's node and expired on every other node in the network -
// including the ones that had to mine it. That asymmetry is what this removes.
//
// Note what this does not do: it does not exempt the transaction from the compound rate limiter. A
// sender flooding compound transactions is still throttled at the door, which is the right place for
// it; this only decides how long a transaction that was already admitted is kept.
func (mp *mempool) raisePriorityIfCompound(transaction *externalapi.DomainTransaction, isHighPriority bool) bool {
	if isHighPriority || !mp.compoundTxRateLimiter.looksLikeCompoundTransaction(transaction) {
		return isHighPriority
	}

	// A zero CompoundTxMinInputsThreshold satisfies "inputs >= threshold" for every transaction. To the
	// rate limiter that is a coherent setting - throttle everything - and it keeps that meaning. Here it
	// would mean every transaction is high priority, and therefore that nothing in this mempool ever
	// expires or is evicted, which is not a throttle but the removal of the pool's only bound. So when
	// the degenerate input rule is the only thing that matched, fall back to the ordinary lifetime; a
	// genuinely large transaction still qualifies on mass.
	if mp.config.CompoundTxMinInputsThreshold == 0 && transaction.Mass <= MaximumStandardTransactionMass/2 {
		return isHighPriority
	}

	log.Debugf("Transaction %s is a compound transaction (%d inputs, mass %d), raising it to high "+
		"priority so that the mempool does not expire it", consensushashing.TransactionID(transaction),
		len(transaction.Inputs), transaction.Mass)
	return true
}
