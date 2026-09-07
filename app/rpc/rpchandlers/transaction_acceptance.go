package rpchandlers

import (
	"github.com/HoosatNetwork/HTND/app/rpc/rpccontext"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
)

// maxChainBlocksSearchedForAcceptance bounds how far up the selected chain to look for the chain
// block that merged a given block. A block is merged within a few chain blocks of itself, so this is
// generous; it exists so a transaction in a block that never gets merged cannot turn one RPC call
// into a walk to the tip.
const maxChainBlocksSearchedForAcceptance = 200

// transactionAcceptance is what a chain block decided about one transaction.
type transactionAcceptance struct {
	// acceptingBlock is the chain block that merged the block carrying the transaction. Nil when no
	// chain block has merged it yet.
	acceptingBlock *externalapi.DomainHash

	// accepted is that block's verdict. A merged transaction can be rejected - a duplicate of one
	// already accepted, or a spend of something already spent - and rejected is a settled answer, not
	// a pending one.
	accepted bool
}

// findTransactionAcceptance answers who accepted a transaction, and whether they did.
//
// Acceptance is a property of the chain block that MERGED the transaction, never of the block that
// contains it. On a DAG a transaction is routinely carried by several blocks, and all but the one on
// the selected chain stay StatusUTXOPendingVerification for good - correctly, because a non-chain
// block never has its own UTXO state resolved. Reading the containing block's status therefore
// reports a transaction the chain accepted long ago as "pending", forever, with its confirmation
// count climbing all the while. That is what a wallet was being told.
//
// So walk the selected chain up from the containing block and ask each chain block's acceptance data
// whether it merged that block; the first that did is the one whose verdict counts.
func findTransactionAcceptance(context *rpccontext.Context, containingBlock *externalapi.DomainHash,
	transactionID *externalapi.DomainTransactionID,
) (*transactionAcceptance, error) {
	chainPath, err := context.Domain.Consensus().GetVirtualSelectedParentChainFromBlock(containingBlock)
	if err != nil {
		// The containing block is not reachable from the selected chain - it has not been merged, so
		// there is no verdict yet rather than a failure.
		return &transactionAcceptance{}, nil
	}

	for i, chainBlock := range chainPath.Added {
		if i >= maxChainBlocksSearchedForAcceptance {
			break
		}
		acceptanceData, err := context.Domain.Consensus().GetBlockAcceptanceData(chainBlock)
		if err != nil {
			// This chain block's acceptance data is not available (it may not be resolved yet). Keep
			// looking rather than concluding anything from its absence.
			continue
		}

		mergedHere, acceptedHere := verdictForTransaction(acceptanceData, transactionID)
		if mergedHere {
			return &transactionAcceptance{acceptingBlock: chainBlock, accepted: acceptedHere}, nil
		}
	}

	return &transactionAcceptance{}, nil
}

// verdictForTransaction reads one chain block's acceptance data for a transaction's fate.
//
// The verdict belongs to the TRANSACTION, not to one copy of it. A chain block merges every block in
// its merge set, so when several of them carry the same transaction its acceptance data holds one
// entry per carrier - exactly one accepted, the rest rejected as duplicates. Reading only the entry
// for the block the transaction happened to be found in therefore reports a perfectly good
// transaction as invalid whenever the block-store scan landed on a duplicate, which on a DAG is most
// of the time. Accepted anywhere in this block's acceptance data means accepted.
func verdictForTransaction(acceptanceData externalapi.AcceptanceData,
	transactionID *externalapi.DomainTransactionID,
) (merged bool, accepted bool) {
	for _, blockAcceptanceData := range acceptanceData {
		for _, transactionAcceptanceData := range blockAcceptanceData.TransactionAcceptanceData {
			if !consensushashing.TransactionID(transactionAcceptanceData.Transaction).Equal(transactionID) {
				continue
			}
			merged = true
			if transactionAcceptanceData.IsAccepted {
				accepted = true
			}
		}
	}
	return merged, accepted
}
