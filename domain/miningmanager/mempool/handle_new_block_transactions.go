package mempool

import (
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/consensushashing"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/transactionhelper"
)

func (mp *mempool) handleNewBlockTransactions(blockTransactions []*externalapi.DomainTransaction) (
	[]*externalapi.DomainTransaction, error,
) {
	// Skip the coinbase transaction
	blockTransactions = blockTransactions[transactionhelper.CoinbaseTransactionIndex+1:]

	acceptedOrphans := make([]*externalapi.DomainTransaction, 0, len(blockTransactions))
	for i := 0; i < len(blockTransactions); i++ {
		transactionID := consensushashing.TransactionID(blockTransactions[i])
		err := mp.removeTransaction(transactionID, false)
		if err != nil {
			return nil, err
		}

		err = mp.removeDoubleSpends(blockTransactions[i])
		if err != nil {
			return nil, err
		}

		err = mp.orphansPool.removeOrphan(transactionID, false)
		if err != nil {
			return nil, err
		}

		acceptedOrphansFromThisTransaction, err := mp.orphansPool.processOrphansAfterAcceptedTransaction(blockTransactions[i])
		if err != nil {
			return nil, err
		}

		acceptedOrphans = append(acceptedOrphans, acceptedOrphansFromThisTransaction...)
	}
	err := mp.orphansPool.expireOrphanTransactions()
	if err != nil {
		return nil, err
	}
	err = mp.transactionsPool.expireOldTransactions()
	if err != nil {
		return nil, err
	}

	return acceptedOrphans, nil
}

func (mp *mempool) removeDoubleSpends(transaction *externalapi.DomainTransaction) error {
	for i := 0; i < len(transaction.Inputs); i++ {
		if redeemer, ok := mp.mempoolUTXOSet.transactionByPreviousOutpoint[transaction.Inputs[i].PreviousOutpoint]; ok {
			err := mp.removeTransaction(redeemer.TransactionID(), true)
			if err != nil {
				return err
			}
		}
	}
	return nil
}
