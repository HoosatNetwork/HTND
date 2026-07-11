package rpchandlers

import (
	"github.com/Hoosat-Oy/HTND/app/appmessage"
	"github.com/Hoosat-Oy/HTND/app/rpc/rpccontext"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/consensushashing"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/transactionid"
	"github.com/Hoosat-Oy/HTND/infrastructure/network/netadapter/router"
)

// HandleGetTransactionStatus handles the respectively named RPC command.
func HandleGetTransactionStatus(context *rpccontext.Context, _ *router.Router, request appmessage.Message) (appmessage.Message, error) {
	getTransactionStatusRequest := request.(*appmessage.GetTransactionStatusRequestMessage)

	transactionID, err := transactionid.FromString(getTransactionStatusRequest.TransactionID)
	if err != nil {
		emptyHash, _ := externalapi.NewDomainHashFromString("")
		errorMessage := appmessage.NewGetTransactionStatusResponseMessage(
			appmessage.TransactionStatusNotFound,
			emptyHash,
			0,
		)
		return errorMessage, nil
	}

	// Check mempool first
	mempoolTransaction, isOrphan, found := context.Domain.MiningManager().GetTransactionNoClone(transactionID, true, true)
	if found && mempoolTransaction != nil {
		emptyHash, _ := externalapi.NewDomainHashFromString("")
		if isOrphan {
			return appmessage.NewGetTransactionStatusResponseMessage(appmessage.TransactionStatusOrphan, emptyHash, 0), nil
		}
		return appmessage.NewGetTransactionStatusResponseMessage(appmessage.TransactionStatusPending, emptyHash, 0), nil
	}

	emptyHash, _ := externalapi.NewDomainHashFromString("")
	// Try to find block
	block, err := context.Domain.Consensus().GetBlockByTransactionID(transactionID)
	if err != nil {
		return appmessage.NewGetTransactionStatusResponseMessage(appmessage.TransactionStatusNotFound, emptyHash, 0), nil
	}

	blockHash := consensushashing.BlockHash(block)

	_, blockChildren, err := context.Domain.Consensus().GetBlockRelations(blockHash)
	if err != nil {
		return nil, err
	}

	if len(blockChildren) == 0 {
		return appmessage.NewGetTransactionStatusResponseMessage(appmessage.TransactionStatusPending, emptyHash, 0), nil
	}

	// Get confirmation info
	selectedParent, err := context.Domain.Consensus().GetVirtualSelectedParent()
	if err != nil {
		return nil, err
	}

	selectedParentInfo, err := context.Domain.Consensus().GetBlockInfo(selectedParent)
	if err != nil {
		return nil, err
	}

	blockInfo, err := context.Domain.Consensus().GetBlockInfo(blockHash)
	if err != nil {
		return nil, err
	}

	confirmations := selectedParentInfo.BlueScore - blockInfo.BlueScore + 1

	// Status logic
	switch {
	case blockInfo.BlockStatus == externalapi.StatusInvalid || blockInfo.BlockStatus == externalapi.StatusDisqualifiedFromChain:
		return appmessage.NewGetTransactionStatusResponseMessage(appmessage.TransactionStatusInvalid, emptyHash, 0), nil

	case blockInfo.BlockStatus == externalapi.StatusHeaderOnly:
		return appmessage.NewGetTransactionStatusResponseMessage(appmessage.TransactionStatusUnknown, emptyHash, confirmations), nil

	case blockInfo.BlockStatus == externalapi.StatusUTXOPendingVerification:
		return appmessage.NewGetTransactionStatusResponseMessage(appmessage.TransactionStatusPending, emptyHash, confirmations), nil

	case blockInfo.BlockStatus == externalapi.StatusUTXOValid:
		_, children, err := context.Domain.Consensus().GetBlockRelations(blockHash)
		if err != nil {
			return nil, err
		}

		childHash, _ := externalapi.NewDomainHashFromString("")
		for _, child := range children {
			childInfo, err := context.Domain.Consensus().GetBlockInfo(child)
			if err != nil {
				return nil, err
			}
			log.Infof("child %s selected parent %s", child, childInfo.SelectedParent)
			isChainBlock, err := context.Domain.Consensus().IsChainBlock(child)
			if err != nil {
				return nil, err
			}
			if isChainBlock && childInfo.SelectedParent.Equal(blockHash) {
				childHash = child
				break
			}
		}
		if confirmations >= 1000 {
			return appmessage.NewGetTransactionStatusResponseMessage(appmessage.TransactionStatusAccepted, childHash, confirmations), nil
		}
		return appmessage.NewGetTransactionStatusResponseMessage(appmessage.TransactionStatusConfirmed, childHash, confirmations), nil

	default:
		return appmessage.NewGetTransactionStatusResponseMessage(appmessage.TransactionStatusUnknown, emptyHash, confirmations), nil
	}
}
