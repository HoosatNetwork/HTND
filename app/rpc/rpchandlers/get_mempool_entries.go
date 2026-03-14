package rpchandlers

import (
	"github.com/Hoosat-Oy/HTND/app/appmessage"
	"github.com/Hoosat-Oy/HTND/app/rpc/rpccontext"
	"github.com/Hoosat-Oy/HTND/infrastructure/network/netadapter/router"
)

// HandleGetMempoolEntries handles the respectively named RPC command
func HandleGetMempoolEntries(context *rpccontext.Context, _ *router.Router, request appmessage.Message) (appmessage.Message, error) {
	getMempoolEntriesRequest := request.(*appmessage.GetMempoolEntriesRequestMessage)

	transactionPoolTransactions, orphanPoolTransactions := context.Domain.MiningManager().AllTransactionsNoClone(!getMempoolEntriesRequest.FilterTransactionPool, getMempoolEntriesRequest.IncludeOrphanPool)
	entries := make([]*appmessage.MempoolEntry, 0, len(transactionPoolTransactions)+len(orphanPoolTransactions))

	if !getMempoolEntriesRequest.FilterTransactionPool {
		for _, transaction := range transactionPoolTransactions {
			rpcTransaction := appmessage.DomainTransactionToRPCTransaction(transaction)
			err := context.PopulateTransactionWithVerboseData(rpcTransaction, transaction, nil)
			if err != nil {
				return nil, err
			}
			entries = append(entries, &appmessage.MempoolEntry{
				Fee:         transaction.Fee,
				Transaction: rpcTransaction,
				IsOrphan:    false,
			})
		}
	}
	if getMempoolEntriesRequest.IncludeOrphanPool {
		for _, transaction := range orphanPoolTransactions {
			rpcTransaction := appmessage.DomainTransactionToRPCTransaction(transaction)
			err := context.PopulateTransactionWithVerboseData(rpcTransaction, transaction, nil)
			if err != nil {
				return nil, err
			}
			entries = append(entries, &appmessage.MempoolEntry{
				Fee:         transaction.Fee,
				Transaction: rpcTransaction,
				IsOrphan:    true,
			})
		}
	}

	return appmessage.NewGetMempoolEntriesResponseMessage(entries), nil
}
