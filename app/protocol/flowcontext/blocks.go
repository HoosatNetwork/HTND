package flowcontext

import (
	"time"

	peerpkg "github.com/Hoosat-Oy/HTND/app/protocol/peer"
	"github.com/Hoosat-Oy/HTND/app/protocol/protocolerrors"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/ruleerrors"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/consensushashing"
	"github.com/pkg/errors"

	"github.com/Hoosat-Oy/HTND/app/appmessage"
)

// OnNewBlock updates the mempool after a new block arrival, and
// relays newly unorphaned transactions and possibly rebroadcast
// manually added transactions when not in IBD.
func (f *FlowContext) OnNewBlock(block *externalapi.DomainBlock) error {

	// hash := consensushashing.BlockHash(block)
	// log.Tracef("OnNewBlock start for block %s", hash)
	// defer log.Tracef("OnNewBlock end for block %s", hash)

	unorphanedBlocks, err := f.UnorphanBlocks(block)
	if err != nil {
		return err
	}

	// log.Debugf("OnNewBlock: block %s unorphaned %d blocks", hash, len(unorphanedBlocks))

	newBlocks := make([]*externalapi.DomainBlock, 0, len(unorphanedBlocks)+1)
	newBlocks = append(newBlocks, block)
	newBlocks = append(newBlocks, unorphanedBlocks...)

	allAcceptedTransactions := make([]*externalapi.DomainTransaction, 0, len(newBlocks))
	for i := 0; i < len(newBlocks); i++ {
		// log.Debugf("OnNewBlock: passing block %s transactions to mining manager", hash)
		acceptedTransactions, err := f.Domain().MiningManager().HandleNewBlockTransactions(newBlocks[i].Transactions)
		if err != nil {
			return err
		}
		allAcceptedTransactions = append(allAcceptedTransactions, acceptedTransactions...)
	}

	return f.broadcastTransactionsAfterBlockAdded(newBlocks, allAcceptedTransactions)
}

// OnNewBlockTemplate calls the handler function whenever a new block template is available for miners.
func (f *FlowContext) OnNewBlockTemplate() error {
	// Clear current template cache. Note we call this even if the handler is nil, in order to keep the
	// state consistent without dependency on external event registration
	f.Domain().MiningManager().ClearBlockTemplate()
	if f.onNewBlockTemplateHandler != nil {
		return f.onNewBlockTemplateHandler()
	}

	return nil
}

// OnPruningPointUTXOSetOverride calls the handler function whenever the UTXO set
// resets due to pruning point change via IBD.
func (f *FlowContext) OnPruningPointUTXOSetOverride() error {
	if f.onPruningPointUTXOSetOverrideHandler != nil {
		return f.onPruningPointUTXOSetOverrideHandler()
	}
	return nil
}

func (f *FlowContext) broadcastTransactionsAfterBlockAdded(
	addedBlocks []*externalapi.DomainBlock, transactionsAcceptedToMempool []*externalapi.DomainTransaction) error {

	// Don't relay transactions when in IBD.
	if f.IsIBDRunning() {
		return nil
	}

	var txIDsToRebroadcast []*externalapi.DomainTransactionID
	if f.shouldRebroadcastTransactions() {
		txsToRebroadcast, err := f.Domain().MiningManager().RevalidateHighPriorityTransactions()
		if err != nil {
			return err
		}
		txIDsToRebroadcast = consensushashing.TransactionIDs(txsToRebroadcast)
		f.lastRebroadcastTime = time.Now()
	}

	totalLen := len(transactionsAcceptedToMempool) + len(txIDsToRebroadcast)
	txIDsToBroadcast := make([]*externalapi.DomainTransactionID, totalLen)

	for i := range transactionsAcceptedToMempool {
		txID := consensushashing.TransactionID(transactionsAcceptedToMempool[i])
		if txID != nil {
			txIDsToBroadcast = append(txIDsToBroadcast, txID)
		}
	}

	offset := len(transactionsAcceptedToMempool)
	for i := 0; i < len(txIDsToRebroadcast); i++ {
		txIDsToBroadcast[offset+i] = txIDsToRebroadcast[i]
	}
	if len(txIDsToBroadcast) == 0 {
		return nil
	}
	return f.EnqueueTransactionIDsForPropagation(txIDsToBroadcast)
}

// SharedRequestedBlocks returns a *blockrelay.SharedRequestedBlocks for sharing
// data about requested blocks between different peers.
func (f *FlowContext) SharedRequestedBlocks() *SharedRequestedBlocks {
	return f.sharedRequestedBlocks
}

// AddBlock adds the given block to the DAG and propagates it.
func (f *FlowContext) AddBlock(block *externalapi.DomainBlock) error {
	if len(block.Transactions) == 0 {
		return protocolerrors.Errorf(false, "cannot add header only block")
	}

	err := f.Domain().Consensus().ValidateAndInsertBlock(block, true, false)
	if err != nil {
		if errors.As(err, &ruleerrors.RuleError{}) {
			log.Debugf("Validation failed for block %s with powhash %s: %s", consensushashing.BlockHash(block), block.PoWHash, err)
		}
		return err
	}
	err = f.OnNewBlockTemplate()
	if err != nil {
		return err
	}
	err = f.OnNewBlock(block)
	if err != nil {
		return err
	}
	return f.Broadcast(appmessage.NewMsgInvBlock(consensushashing.BlockHash(block)))
}

// IsIBDRunning returns true if IBD is currently marked as running
func (f *FlowContext) IsIBDRunning() bool {
	f.ibdPeerMutex.RLock()
	defer f.ibdPeerMutex.RUnlock()

	return f.ibdPeer != nil
}

// TrySetIBDRunning attempts to set `isInIBD`. Returns false
// if it is already set
func (f *FlowContext) TrySetIBDRunning(ibdPeer *peerpkg.Peer) bool {
	f.ibdPeerMutex.Lock()
	defer f.ibdPeerMutex.Unlock()

	if f.ibdPeer != nil {
		return false
	}
	f.ibdPeer = ibdPeer

	return true
}

// UnsetIBDRunning unsets isInIBD
func (f *FlowContext) UnsetIBDRunning() {
	f.ibdPeerMutex.Lock()
	defer f.ibdPeerMutex.Unlock()

	if f.ibdPeer == nil {
		log.Infof("attempted to unset isInIBD when it was not set to begin with")
	}

	f.ibdPeer = nil
}

// IBDPeer returns the current IBD peer or null if the node is not
// in IBD
func (f *FlowContext) IBDPeer() *peerpkg.Peer {
	f.ibdPeerMutex.RLock()
	defer f.ibdPeerMutex.RUnlock()

	return f.ibdPeer
}
