package rpchandlers

import (
	"github.com/HoosatNetwork/HTND/app/appmessage"
	"github.com/HoosatNetwork/HTND/app/rpc/rpccontext"
	"github.com/HoosatNetwork/HTND/domain/consensus"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/transactionid"
	"github.com/HoosatNetwork/HTND/infrastructure/network/netadapter/router"
	"github.com/pkg/errors"
)

// HandleGetTransactionStatus handles the respectively named RPC command.
// confirmationsConsideredSettled is where an accepted transaction is additionally reported as
// confirmed. Accepted means a chain block took it; confirmed means enough chain has been built on
// top that reversal is not a practical concern. The two used to be the wrong way round - fewer than
// 1000 confirmations reported "confirmed" and more reported "accepted" - so a transaction became
// less settled-sounding the deeper it was buried.
const confirmationsConsideredSettled = 1000

func HandleGetTransactionStatus(context *rpccontext.Context, _ *router.Router, request appmessage.Message) (appmessage.Message, error) {
	getTransactionStatusRequest := request.(*appmessage.GetTransactionStatusRequestMessage)

	transactionID, err := transactionid.FromString(getTransactionStatusRequest.TransactionID)
	if err != nil {
		// An unparseable id is a malformed request, not a transaction the node has never seen.
		// Answering "not found" told the caller something about the network when the truth was about
		// the question: a transaction id that lost a character in a copy-paste came back as NOT_FOUND,
		// indistinguishable from a transaction that genuinely had not propagated, and sent an
		// investigation after the network instead of the typo.
		response := &appmessage.GetTransactionStatusResponseMessage{
			Status: appmessage.TransactionStatusUnknown,
		}
		response.Error = appmessage.RPCErrorf(
			"%q is not a valid transaction id: %s. A transaction id is 64 hexadecimal characters; "+
				"this one has %d", getTransactionStatusRequest.TransactionID, err,
			len(getTransactionStatusRequest.TransactionID))
		return response, nil
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
		// Only a completed search that found nothing is "not found". A search that could not be
		// completed is a fault in this node and has to be reported as one: answering "not found" tells
		// a wallet the transaction does not exist here, which may be false, and which it may act on by
		// rebroadcasting or by treating its funds as unspent.
		if errors.Is(err, consensus.ErrTransactionNotInAnyBlock) {
			return appmessage.NewGetTransactionStatusResponseMessage(
				appmessage.TransactionStatusNotFound, emptyHash, 0), nil
		}
		return nil, err
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

	if blockInfo.BlockStatus == externalapi.StatusInvalid {
		return appmessage.NewGetTransactionStatusResponseMessage(appmessage.TransactionStatusInvalid, emptyHash, 0), nil
	}

	// Whether a transaction was accepted is decided by the chain block that MERGED the block carrying
	// it, not by that block's own status. Most blocks carrying a transaction are not on the selected
	// chain and stay StatusUTXOPendingVerification permanently, which is correct for them and says
	// nothing about the transaction. Reading the containing block's status reported transactions the
	// chain had accepted as "pending" indefinitely, with their confirmation count climbing.
	acceptance, err := findTransactionAcceptance(context, blockHash, transactionID)
	if err != nil {
		return nil, err
	}

	if acceptance.inconclusive {
		// The node stopped looking before reaching a verdict. Unknown is the only honest answer: saying
		// "pending" would be a claim about the transaction made from a fact about the search.
		response := &appmessage.GetTransactionStatusResponseMessage{
			Status:        appmessage.TransactionStatusUnknown,
			Confirmations: confirmations,
		}
		response.Error = appmessage.RPCErrorf("could not determine whether transaction %s was accepted: "+
			"the search for the chain block that merged it was cut short", transactionID)
		return response, nil
	}
	if acceptance.acceptingBlock == nil {
		// No chain block has merged it yet. Genuinely pending, whatever the containing block's status.
		return appmessage.NewGetTransactionStatusResponseMessage(
			appmessage.TransactionStatusPending, emptyHash, confirmations), nil
	}
	return transactionStatusResponse(context, acceptance)
}

// transactionStatusResponse turns a chain block's verdict into a response, counting confirmations
// from the block that accepted the transaction rather than from whichever block happened to carry
// it. Both lookup paths end here so they cannot drift apart in what they report.
func transactionStatusResponse(context *rpccontext.Context, acceptance *transactionAcceptance,
) (appmessage.Message, error) {
	selectedParent, err := context.Domain.Consensus().GetVirtualSelectedParent()
	if err != nil {
		return nil, err
	}
	selectedParentInfo, err := context.Domain.Consensus().GetBlockInfo(selectedParent)
	if err != nil {
		return nil, err
	}
	acceptingBlockInfo, err := context.Domain.Consensus().GetBlockInfo(acceptance.acceptingBlock)
	if err != nil {
		return nil, err
	}
	confirmations := selectedParentInfo.BlueScore - acceptingBlockInfo.BlueScore + 1

	switch {
	case !acceptance.accepted:
		// Merged and rejected - a duplicate of one already accepted, or a spend of something already
		// spent. That is a settled answer and must not be reported as still pending.
		return appmessage.NewGetTransactionStatusResponseMessage(
			appmessage.TransactionStatusInvalid, acceptance.acceptingBlock, confirmations), nil
	case confirmations >= confirmationsConsideredSettled:
		return appmessage.NewGetTransactionStatusResponseMessage(
			appmessage.TransactionStatusConfirmed, acceptance.acceptingBlock, confirmations), nil
	default:
		return appmessage.NewGetTransactionStatusResponseMessage(
			appmessage.TransactionStatusAccepted, acceptance.acceptingBlock, confirmations), nil
	}
}
