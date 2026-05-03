package rpchandlers

import (
	"github.com/Hoosat-Oy/HTND/app/appmessage"
	"github.com/Hoosat-Oy/HTND/app/rpc/rpccontext"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/consensushashing"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/transactionid"
	"github.com/Hoosat-Oy/HTND/infrastructure/network/netadapter/router"
)

// HandleGetTransactionStatus handles the respectively named RPC command.
func HandleGetTransactionStatus(context *rpccontext.Context, _ *router.Router, request appmessage.Message) (appmessage.Message, error) {
	getTransactionStatusRequest := request.(*appmessage.GetTransactionStatusRequestMessage)

	transactionID, err := transactionid.FromString(getTransactionStatusRequest.TransactionID)
	if err != nil {
		errorMessage := &appmessage.GetTransactionStatusResponseMessage{}
		errorMessage.Error = appmessage.RPCErrorf("Transaction ID could not be parsed: %s", err)
		return errorMessage, nil
	}

	consensus := context.Domain.Consensus()
	block, err := consensus.GetBlockByTransactionID(transactionID)
	if err == nil {
		blockHash := consensushashing.BlockHash(block)
		isChainBlock, err := consensus.IsChainBlock(blockHash)
		if err != nil {
			return nil, err
		}

		if !isChainBlock {
			return appmessage.NewGetTransactionStatusResponseMessage(appmessage.TransactionStatusAccepted, 0), nil
		}

		selectedParent, err := consensus.GetVirtualSelectedParent()
		if err != nil {
			return nil, err
		}
		selectedParentInfo, err := consensus.GetBlockInfo(selectedParent)
		if err != nil {
			return nil, err
		}
		blockInfo, err := consensus.GetBlockInfo(blockHash)
		if err != nil {
			return nil, err
		}

		confirmations := selectedParentInfo.BlueScore - blockInfo.BlueScore + 1
		return appmessage.NewGetTransactionStatusResponseMessage(appmessage.TransactionStatusConfirmed, confirmations), nil
	}

	mempoolTransaction, isOrphan, found := context.Domain.MiningManager().GetTransactionNoClone(transactionID, true, true)
	if found && mempoolTransaction != nil {
		status := appmessage.TransactionStatusPending
		if isOrphan {
			status = appmessage.TransactionStatusOrphan
		}
		return appmessage.NewGetTransactionStatusResponseMessage(status, 0), nil
	}

	return appmessage.NewGetTransactionStatusResponseMessage(appmessage.TransactionStatusNotFound, 0), nil
}
