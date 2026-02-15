package rpchandlers

import (
	"github.com/Hoosat-Oy/HTND/app/appmessage"
	"github.com/Hoosat-Oy/HTND/app/rpc/rpccontext"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/transactionid"
	"github.com/Hoosat-Oy/HTND/infrastructure/network/netadapter/router"
	"github.com/pkg/errors"
)

// HandleGetBlockByTransactionID handles the respectively named RPC command
func HandleGetBlockByTransactionID(context *rpccontext.Context, _ *router.Router, request appmessage.Message) (appmessage.Message, error) {
	getBlockByTransactionIDRequest := request.(*appmessage.GetBlockByTransactionIDRequestMessage)
	if context.Config.SafeRPC {
		log.Warn("GetBlockByTransactionID RPC command called while node in safe RPC mode -- ignoring.")
		response := appmessage.NewGetBlockByTransactionIDResponseMessage()
		response.Error =
			appmessage.RPCErrorf("GetBlockByTransactionID RPC command called while node in safe RPC mode")
		return response, nil
	}

	// Parse the transaction ID
	transactionID, err := transactionid.FromString(getBlockByTransactionIDRequest.TransactionID)
	if err != nil {
		errorMessage := &appmessage.GetBlockByTransactionIDResponseMessage{}
		errorMessage.Error = appmessage.RPCErrorf("Transaction ID could not be parsed: %s", err)
		return errorMessage, nil
	}

	// Search for the block containing this transaction
	block, err := context.Domain.Consensus().GetBlockByTransactionID(transactionID)
	if err != nil {
		errorMessage := &appmessage.GetBlockByTransactionIDResponseMessage{}
		errorMessage.Error = appmessage.RPCErrorf("Transaction %s not found in any block", transactionID)
		return errorMessage, nil
	}

	response := appmessage.NewGetBlockByTransactionIDResponseMessage()

	if getBlockByTransactionIDRequest.IncludeTransactions {
		response.Block = appmessage.DomainBlockToRPCBlock(block)
	} else {
		response.Block = appmessage.DomainBlockToRPCBlock(&externalapi.DomainBlock{Header: block.Header})
	}

	err = context.PopulateBlockWithVerboseData(response.Block, block.Header, block, getBlockByTransactionIDRequest.IncludeTransactions)
	if err != nil {
		if errors.Is(err, rpccontext.ErrBuildBlockVerboseDataInvalidBlock) {
			errorMessage := &appmessage.GetBlockByTransactionIDResponseMessage{}
			errorMessage.Error = appmessage.RPCErrorf("Block containing transaction %s is invalid", transactionID)
			return errorMessage, nil
		}
		errorMessage := &appmessage.GetBlockByTransactionIDResponseMessage{}
		errorMessage.Error = appmessage.RPCErrorf("Block containing transaction %s caused an error", transactionID)
		return errorMessage, err
	}

	return response, nil
}
