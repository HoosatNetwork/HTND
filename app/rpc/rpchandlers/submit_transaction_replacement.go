package rpchandlers

import (
	"github.com/Hoosat-Oy/HTND/app/appmessage"
	"github.com/Hoosat-Oy/HTND/app/rpc/rpccontext"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/consensushashing"
	"github.com/Hoosat-Oy/HTND/domain/miningmanager/mempool"
	"github.com/Hoosat-Oy/HTND/infrastructure/network/netadapter/router"
	"github.com/pkg/errors"
)

// HandleSubmitTransactionReplacement handles the respectively named RPC command.
func HandleSubmitTransactionReplacement(context *rpccontext.Context, _ *router.Router, request appmessage.Message) (appmessage.Message, error) {
	submitTransactionReplacementRequest := request.(*appmessage.SubmitTransactionReplacementRequestMessage)

	domainTransaction, err := appmessage.RPCTransactionToDomainTransaction(submitTransactionReplacementRequest.Transaction)
	if err != nil {
		errorMessage := &appmessage.SubmitTransactionReplacementResponseMessage{}
		errorMessage.Error = appmessage.RPCErrorf("Could not parse transaction: %s", err)
		return errorMessage, nil
	}

	transactionID := consensushashing.TransactionID(domainTransaction)
	isHighPriority := true
	if submitTransactionReplacementRequest.IsHighPriority != nil {
		isHighPriority = *submitTransactionReplacementRequest.IsHighPriority
	}

	replacedTransaction, err := context.ProtocolManager.AddTransactionReplacementWithPriority(domainTransaction, isHighPriority)
	if err != nil {
		if !errors.As(err, &mempool.RuleError{}) {
			return nil, err
		}

		log.Debugf("Rejected transaction replacement %s: %s", transactionID, err)
		errorMessage := appmessage.NewSubmitTransactionReplacementResponseMessage(transactionID.String())
		errorMessage.Error = appmessage.RPCErrorf("Rejected transaction %s: %s", transactionID, err)
		return errorMessage, nil
	}

	response := appmessage.NewSubmitTransactionReplacementResponseMessage(transactionID.String())
	if replacedTransaction != nil {
		response.ReplacedTransaction = appmessage.DomainTransactionToRPCTransaction(replacedTransaction)
	}
	return response, nil
}
