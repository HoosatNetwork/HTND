package rpchandlers

import (
	"github.com/Hoosat-Oy/HTND/app/appmessage"
	"github.com/Hoosat-Oy/HTND/app/rpc/rpccontext"
	"github.com/Hoosat-Oy/HTND/domain/consensus/database"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/infrastructure/network/netadapter/router"
	"github.com/pkg/errors"
)

// HandleGetVirtualSelectedParentChainFromBlock handles the respectively named RPC command
func HandleGetVirtualSelectedParentChainFromBlock(context *rpccontext.Context, _ *router.Router, request appmessage.Message) (appmessage.Message, error) {
	getVirtualSelectedParentChainFromBlockRequest := request.(*appmessage.GetVirtualSelectedParentChainFromBlockRequestMessage)

	startHash, err := externalapi.NewDomainHashFromString(getVirtualSelectedParentChainFromBlockRequest.StartHash)
	if err != nil {
		errorMessage := &appmessage.GetVirtualSelectedParentChainFromBlockResponseMessage{}
		errorMessage.Error = appmessage.RPCErrorf("Could not parse startHash: %s", err)
		return errorMessage, nil
	}

	virtualSelectedParentChain, err := context.Domain.Consensus().GetVirtualSelectedParentChainFromBlock(startHash)
	if err != nil {
		response := &appmessage.GetVirtualSelectedParentChainFromBlockResponseMessage{}
		response.Error = appmessage.RPCErrorf("Could not build virtual "+
			"selected parent chain from %s: %s", getVirtualSelectedParentChainFromBlockRequest.StartHash, err)
		return response, nil
	}

	chainChangedNotification, err := context.ConvertVirtualSelectedParentChainChangesToChainChangedNotificationMessage(virtualSelectedParentChain, getVirtualSelectedParentChainFromBlockRequest.IncludeAcceptedTransactionIDs)
	if err != nil {
		// Initialize default empty values for the response to avoid nil pointer dereference
		removedChainBlockHashes := []string{}
		addedChainBlockHashes := []string{}
		acceptedTransactionIDs := []*appmessage.AcceptedTransactionIDs{}

		// If chainChangedNotification is not nil, use its values
		if chainChangedNotification != nil {
			if chainChangedNotification.RemovedChainBlockHashes != nil {
				removedChainBlockHashes = chainChangedNotification.RemovedChainBlockHashes
			}
			if chainChangedNotification.AddedChainBlockHashes != nil {
				addedChainBlockHashes = chainChangedNotification.AddedChainBlockHashes
			}
			if chainChangedNotification.AcceptedTransactionIDs != nil {
				acceptedTransactionIDs = chainChangedNotification.AcceptedTransactionIDs
			}
		}

		response := appmessage.NewGetVirtualSelectedParentChainFromBlockResponseMessage(
			removedChainBlockHashes,
			addedChainBlockHashes,
			acceptedTransactionIDs,
		)
		if errors.Is(err, database.ErrNotFound) {
			response.Error = appmessage.RPCErrorf("Acceptance data not found for one or more blocks in the chain starting from %s", getVirtualSelectedParentChainFromBlockRequest.StartHash)
			return response, nil
		}
		return nil, err
	}
	response := appmessage.NewGetVirtualSelectedParentChainFromBlockResponseMessage(
		chainChangedNotification.RemovedChainBlockHashes, chainChangedNotification.AddedChainBlockHashes,
		chainChangedNotification.AcceptedTransactionIDs)
	return response, nil
}
