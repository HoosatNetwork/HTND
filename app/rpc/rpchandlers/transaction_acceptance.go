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
// Generous rather than tight: Added runs from the common ancestor upward, so the block that merged a
// given block is normally within a handful of entries - but a transaction carried only by blocks off
// the selected chain can push the common ancestor far below it, and cutting the search short there
// produced a confident wrong answer instead of an admission that the node had stopped looking.
const maxChainBlocksSearchedForAcceptance = 20000

// maxChainBlocksSearchedFromTip bounds the fast path that looks for a transaction in recently
// accepted chain blocks. A transaction submitted to a node is mined within seconds on this chain, so
// the block that accepted it is a short walk back from virtual - and finding it there costs a few
// acceptance-data reads instead of a scan of every block the node holds.
const maxChainBlocksSearchedFromTip = 2000

// findRecentlyAcceptedTransaction looks for a transaction in the acceptance data of chain blocks near
// the tip, walking back from virtual's selected parent.
//
// The general lookup is a scan of the entire block store, taking the consensus lock once per block.
// On a node with millions of blocks that is slow enough to be unusable for the case people actually
// ask about - "I just submitted this, where is it" - and any error along the way used to come back as
// "not found", which reads as an answer about the transaction rather than about the search. A
// transaction accepted in the last few thousand chain blocks is found here in a fraction of the time,
// and the answer carries the accepting block with it.
//
// Returns nil when the transaction is not in that window, which is not a verdict - the caller falls
// back to the full search.
func findRecentlyAcceptedTransaction(context *rpccontext.Context,
	transactionID *externalapi.DomainTransactionID,
) *transactionAcceptance {
	current, err := context.Domain.Consensus().GetVirtualSelectedParent()
	if err != nil {
		return nil
	}

	for i := 0; i < maxChainBlocksSearchedFromTip && current != nil; i++ {
		acceptanceData, err := context.Domain.Consensus().GetBlockAcceptanceData(current)
		if err == nil {
			if merged, accepted := verdictForTransaction(acceptanceData, transactionID); merged {
				return &transactionAcceptance{acceptingBlock: current, accepted: accepted}
			}
		}

		blockInfo, err := context.Domain.Consensus().GetBlockInfo(current)
		if err != nil || blockInfo.SelectedParent == nil {
			return nil
		}
		current = blockInfo.SelectedParent
	}
	return nil
}

// transactionAcceptance is what a chain block decided about one transaction.
type transactionAcceptance struct {
	// acceptingBlock is the chain block that merged the block carrying the transaction. Nil when no
	// verdict was found.
	acceptingBlock *externalapi.DomainHash

	// accepted is that block's verdict. A merged transaction can be rejected - a duplicate of one
	// already accepted, or a spend of something already spent - and rejected is a settled answer, not
	// a pending one.
	accepted bool

	// inconclusive means the search ran out of room before reaching a verdict, rather than
	// establishing that no chain block has merged the transaction. The two must not be reported the
	// same way: "not merged yet" is a claim about the transaction, while this is a statement about how
	// far the node looked. Reporting a truncated search as "pending" is how one node came back PENDING
	// for a transaction another node reported CONFIRMED, with 15,000 confirmations on both.
	inconclusive bool
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

	var firstRejection *externalapi.DomainHash
	for i, chainBlock := range chainPath.Added {
		if i >= maxChainBlocksSearchedForAcceptance {
			// Out of room, not out of chain. Say so rather than implying the transaction is unmerged.
			return &transactionAcceptance{inconclusive: true}, nil
		}
		acceptanceData, err := context.Domain.Consensus().GetBlockAcceptanceData(chainBlock)
		if err != nil {
			// This chain block's acceptance data is not available (it may not be resolved yet). Keep
			// looking rather than concluding anything from its absence.
			continue
		}

		mergedHere, acceptedHere := verdictForTransaction(acceptanceData, transactionID)
		if !mergedHere {
			continue
		}
		if acceptedHere {
			return &transactionAcceptance{acceptingBlock: chainBlock, accepted: true}, nil
		}
		// Merged and rejected here - but that is not yet the answer. The same transaction is carried
		// by several blocks on a DAG, and once one chain block accepts it every later chain block
		// that merges another copy rejects that copy. Stopping at the first mention therefore
		// reported a transaction as invalid whenever the walk happened to reach a later copy first,
		// and which copy is reached first depends on which containing block the block-store scan
		// returned - an arbitrary order that differs between nodes. Two nodes were observed
		// disagreeing on thirty of forty-one transactions this way.
		//
		// So remember the rejection and keep looking. Acceptance anywhere on the chain settles it;
		// only a walk that finishes having seen nothing but rejections reports one.
		if firstRejection == nil {
			firstRejection = chainBlock
		}
	}

	if firstRejection != nil {
		return &transactionAcceptance{acceptingBlock: firstRejection, accepted: false}, nil
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
