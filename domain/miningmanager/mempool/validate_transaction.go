package mempool

import (
	"fmt"

	"github.com/HoosatNetwork/HTND/domain/consensus/utils/constants"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
)

func (mp *mempool) validateTransactionPreUTXOEntry(transaction *externalapi.DomainTransaction) error {
	err := mp.validateTransactionInIsolation(transaction)
	if err != nil {
		return err
	}

	if err := mp.mempoolUTXOSet.checkDoubleSpends(transaction); err != nil {
		return err
	}
	return nil
}

func (mp *mempool) validateTransactionInIsolation(transaction *externalapi.DomainTransaction) error {
	transactionID := consensushashing.TransactionID(transaction)
	if _, ok := mp.transactionsPool.allTransactions[*transactionID]; ok {
		return transactionRuleError(RejectDuplicate,
			fmt.Sprintf("transaction %s is already in the mempool", transactionID))
	}

	if !mp.config.AcceptNonStandard {
		if err := mp.checkTransactionStandardInIsolation(transaction); err != nil {
			// Attempt to extract a reject code from the error so
			// it can be retained. When not possible, fall back to
			// a non standard error.
			rejectCode, found := extractRejectCode(err)
			if !found {
				rejectCode = RejectNonstandard
			}
			str := fmt.Sprintf("transaction %s is not standard: %s", transactionID, err)
			return transactionRuleError(rejectCode, str)
		}
	}

	return nil
}

func (mp *mempool) validateTransactionInContext(transaction *externalapi.DomainTransaction) error {
	hasCoinbaseInput := false
	for _, input := range transaction.Inputs {
		if input.UTXOEntry.IsCoinbase() {
			hasCoinbaseInput = true
			break
		}
	}

	// Check wallet freezing
	if isFrozen, frozenAddresses := mp.walletFreezingManager.isWalletFrozen(transaction); isFrozen {
		txID := consensushashing.TransactionID(transaction)
		log.Debugf("Rejected transaction %s from frozen wallet(s) (addresses: %v, outputs: %d)",
			txID, frozenAddresses, len(transaction.Outputs))
		return transactionRuleError(RejectFreezedWallet,
			fmt.Sprintf("Transaction rejected from frozen wallet addresses: %v", frozenAddresses))
	}

	// Check compound transaction rate limiting
	if isRateLimited, rateLimitedAddresses := mp.compoundTxRateLimiter.isRateLimited(transaction); isRateLimited {
		txID := consensushashing.TransactionID(transaction)
		log.Debugf("Rejected compound transaction %s from mempool due to rate limiting (addresses: %v, inputs: %d)",
			txID, rateLimitedAddresses, len(transaction.Inputs))
		return transactionRuleError(RejectRateLimit,
			fmt.Sprintf("Compound transaction rate limit exceeded for addresses: %v", rateLimitedAddresses))
	}

	numExtraOuts := len(transaction.Outputs) - len(transaction.Inputs)
	if !hasCoinbaseInput && numExtraOuts > 2 && transaction.Fee < uint64(numExtraOuts)*constants.SompiPerHoosat {
		log.Warnf("Rejected spam tx %s from mempool (%d outputs)", consensushashing.TransactionID(transaction), len(transaction.Outputs))
		return transactionRuleError(RejectSpamTx, fmt.Sprintf("Rejected spam tx %s from mempool", consensushashing.TransactionID(transaction)))
	}

	if !mp.config.AcceptNonStandard {
		err := mp.checkTransactionStandardInContext(transaction)
		if err != nil {
			// Attempt to extract a reject code from the error so
			// it can be retained. When not possible, fall back to
			// a non standard error.
			rejectCode, found := extractRejectCode(err)
			if !found {
				rejectCode = RejectNonstandard
			}
			str := fmt.Sprintf("transaction inputs %s are not standard: %s",
				consensushashing.TransactionID(transaction), err)
			return transactionRuleError(rejectCode, str)
		}
	}

	return nil
}
