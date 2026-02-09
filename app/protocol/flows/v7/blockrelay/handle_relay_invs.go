package blockrelay

import (
	"time"

	"github.com/Hoosat-Oy/HTND/app/appmessage"
	"github.com/Hoosat-Oy/HTND/app/protocol/common"
	"github.com/Hoosat-Oy/HTND/app/protocol/flowcontext"
	peerpkg "github.com/Hoosat-Oy/HTND/app/protocol/peer"
	"github.com/Hoosat-Oy/HTND/app/protocol/protocolerrors"
	"github.com/Hoosat-Oy/HTND/domain"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/ruleerrors"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/consensushashing"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/constants"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/hashset"
	"github.com/Hoosat-Oy/HTND/infrastructure/config"
	"github.com/Hoosat-Oy/HTND/infrastructure/db/database"
	"github.com/Hoosat-Oy/HTND/infrastructure/network/connmanager"
	"github.com/Hoosat-Oy/HTND/infrastructure/network/netadapter"
	"github.com/Hoosat-Oy/HTND/infrastructure/network/netadapter/router"
	"github.com/pkg/errors"
)

// orphanResolutionRange is the maximum amount of blockLocator hashes
// to search for known blocks. See isBlockInOrphanResolutionRange for
// further details
var orphanResolutionRange uint32 = 5

// RelayInvsContext is the interface for the context needed for the HandleRelayInvs flow.
type RelayInvsContext interface {
	Domain() domain.Domain
	Config() *config.Config
	OnNewBlock(block *externalapi.DomainBlock) error
	OnNewBlockTemplate() error
	OnPruningPointUTXOSetOverride() error
	SharedRequestedBlocks() *flowcontext.SharedRequestedBlocks
	Broadcast(message appmessage.Message) error
	AddOrphan(orphanBlock *externalapi.DomainBlock)
	GetOrphanRoots(orphanHash *externalapi.DomainHash) ([]*externalapi.DomainHash, bool, error)
	IsOrphan(blockHash *externalapi.DomainHash) bool
	IsIBDRunning() bool
	IsRecoverableError(err error) bool
	IsNearlySynced() (bool, error)
}

type invRelayBlock struct {
	Hash         *externalapi.DomainHash
	IsOrphanRoot bool
}

type handleRelayInvsFlow struct {
	RelayInvsContext
	incomingRoute, outgoingRoute *router.Route
	peer                         *peerpkg.Peer
	connectionManager            *connmanager.ConnectionManager
	netConnection                *netadapter.NetConnection
	invsQueue                    []invRelayBlock
}

// HandleRelayInvs listens to appmessage.MsgInvRelayBlock messages, requests their corresponding blocks if they
// are missing, adds them to the DAG and propagates them to the rest of the network.
func HandleRelayInvs(context RelayInvsContext, connectionManager *connmanager.ConnectionManager, netConnection *netadapter.NetConnection, incomingRoute *router.Route, outgoingRoute *router.Route,
	peer *peerpkg.Peer) error {

	flow := &handleRelayInvsFlow{
		RelayInvsContext:  context,
		incomingRoute:     incomingRoute,
		outgoingRoute:     outgoingRoute,
		peer:              peer,
		connectionManager: connectionManager,
		netConnection:     netConnection,
		invsQueue:         make([]invRelayBlock, 0),
	}
	err := flow.start()
	// Currently, HandleRelayInvs flow is the only place where IBD is triggered, so the channel can be closed now
	close(peer.IBDRequestChannel())
	return err
}

const (
	maxOffenses      = 5
	banThresholdSecs = 300
)

var offenseTracker = make(map[string][]time.Time)

func (flow *handleRelayInvsFlow) banConnection(offenseTimesOverrule bool) {
	address := flow.netConnection.Address()
	now := time.Now()

	// Track offenses
	offenseTimes := offenseTracker[address]
	offenseTimes = append(offenseTimes, now)

	// Remove old offenses outside the threshold window
	var recentOffenses []time.Time
	for _, t := range offenseTimes {
		if now.Sub(t).Seconds() <= banThresholdSecs {
			recentOffenses = append(recentOffenses, t)
		}
	}
	offenseTracker[address] = recentOffenses

	if len(recentOffenses) >= maxOffenses || offenseTimesOverrule {
		log.Infof("Banning connection: %s due to exceeding offense threshold", address)
		_ = flow.connectionManager.Ban(flow.netConnection)
		isBanned, _ := flow.connectionManager.IsBanned(flow.netConnection)
		if isBanned {
			log.Infof("Peer %s is banned. Disconnecting...", flow.netConnection.NetAddress().IP)
			flow.netConnection.Disconnect()
			delete(offenseTracker, address) // Clean up after ban
			return
		}
	} else {
		log.Infof("Peer %s offense recorded (%d/%d within threshold window)", address, len(recentOffenses), maxOffenses)
	}
}

func (flow *handleRelayInvsFlow) start() error {
	for {
		log.Debugf("Waiting for inv")
		inv, err := flow.readInv()
		if err != nil {
			return err
		}

		log.Debugf("Got relay inv for block %s", inv.Hash)
		exists, err := flow.Domain().Consensus().HasBlock(inv.Hash)
		if err != nil {
			return err
		}
		if exists {
			log.Debugf("Don't process, Ignoring duplicate block %s, from %s", inv.Hash, flow.netConnection.Address())
			continue
		}
		blockInfo, err := flow.Domain().Consensus().GetBlockInfo(inv.Hash)
		if err != nil {
			// Treat database not-found as "block doesn't exist" rather than a fatal flow error.
			// Returning database not-found here would bubble up and cause FlowContext.HandleError
			// to panic (since it's not a protocol error). It's safer to proceed as if the
			// block does not exist and continue processing.
			if database.IsNotFoundError(err) {
				log.Debugf("GetBlockInfo returned not-found for %s; treating as non-existing block", inv.Hash)
				continue
			} else {
				return err
			}
		}
		if blockInfo.Exists && blockInfo.BlockStatus != externalapi.StatusHeaderOnly {
			if blockInfo.BlockStatus == externalapi.StatusInvalid {
				log.Debugf("Sent inv of an invalid block %s", inv.Hash)
				flow.banConnection(false)
				continue
			}
			log.Debugf("Block %s already exists. continuing...", inv.Hash)
			continue
		}

		isGenesisVirtualSelectedParent, err := flow.isGenesisVirtualSelectedParent()
		if err != nil {
			return err
		}

		if flow.IsOrphan(inv.Hash) {
			if flow.Config().NetParams().DisallowDirectBlocksOnTopOfGenesis && !flow.Config().AllowSubmitBlockWhenNotSynced && isGenesisVirtualSelectedParent {
				log.Infof("Cannot process orphan %s for a node with only the genesis block. The node needs to IBD to the recent pruning point before normal operation can resume.", inv.Hash)
				continue
			}

			log.Debugf("Block %s is a known orphan. Requesting its missing ancestors", inv.Hash)
			err := flow.AddOrphanRootsToQueue(inv.Hash)
			if err != nil {
				return err
			}
			continue
		}

		if flow.IsIBDRunning() {
			// flow.unreadInv(inv)
			// time.Sleep(100 * time.Millisecond)
			// log.Debugf("Skipping inv hash %s while IBD is in progress.", inv.Hash)
			// continue
			isNearlySynced, err := flow.IsNearlySynced()
			if err != nil {
				return err
			}
			if !isNearlySynced {
				log.Debugf("Got block while in IBD and the node is out of sync. Continuing...")
				continue
			}
		}

		log.Debugf("Requesting block %s", inv.Hash)
		block, exists, err := flow.requestBlock(inv.Hash)
		if err != nil {
			return err
		}
		if exists {
			log.Debugf("Aborting requesting block %s because it already exists", inv.Hash)
			continue
		}
		if block.PoWHash == "" && block.Header.Version() >= constants.BanMinVersion {
			flow.banConnection(false)
		}
		if !flow.IsIBDRunning() {
			daaScore := block.Header.DAAScore()
			var version uint16 = 1
			for _, powScore := range flow.Config().ActiveNetParams.POWScores {
				if daaScore >= powScore {
					version = version + 1
				}
			}
			constants.SetBlockVersion(version)
			if block.Header.Version() != constants.GetBlockVersion() {
				log.Infof("Cannot process %s, Wrong block version %d, it should be %d", consensushashing.BlockHash(block), block.Header.Version(), constants.GetBlockVersion())
				log.Infof("Unprocessable block relayed by %s", flow.netConnection.NetAddress().String())
				if block.Header.Version() >= constants.BanMinVersion {
					flow.banConnection(false)
				}
				continue
			}
		}

		err = flow.banIfBlockIsHeaderOnly(block)
		if err != nil {
			return err
		}

		if flow.Config().NetParams().DisallowDirectBlocksOnTopOfGenesis && !flow.Config().AllowSubmitBlockWhenNotSynced && !flow.Config().Devnet && flow.isChildOfGenesis(block) {
			log.Infof("Cannot process %s because it's a direct child of genesis.", consensushashing.BlockHash(block))
			continue
		}

		// Note we do not apply the heuristic below if inv was queued as an orphan root, since
		// that means the process started by a proper and relevant relay block
		if !inv.IsOrphanRoot {
			// Check bounded merge depth to avoid requesting irrelevant data which cannot be merged under virtual
			virtualMergeDepthRoot, err := flow.Domain().Consensus().VirtualMergeDepthRoot()
			if err != nil {
				return err
			}
			if !virtualMergeDepthRoot.Equal(model.VirtualGenesisBlockHash) {
				mergeDepthRootHeader, err := flow.Domain().Consensus().GetBlockHeader(virtualMergeDepthRoot)
				if err != nil {
					return err
				}
				// Since `BlueWork` respects topology, this condition means that the relay
				// block is not in the future of virtual's merge depth root, and thus cannot be merged unless
				// other valid blocks Kosherize it, in which case it will be obtained once the merger is relayed
				if block.Header.BlueWork().Cmp(mergeDepthRootHeader.BlueWork()) <= 0 {
					log.Debugf("Block %s has lower blue work than virtual's merge root %s (%d <= %d), hence we are skipping it", inv.Hash, virtualMergeDepthRoot, block.Header.BlueWork(), mergeDepthRootHeader.BlueWork())
					continue
				}
			}
		}
		log.Debugf("Processing block %s", inv.Hash)
		oldVirtualInfo, err := flow.Domain().Consensus().GetVirtualInfo()
		if err != nil {
			// If virtual info is missing in the DB, treat it as a transient/missing data
			// and skip processing this inv rather than returning an error that will
			// bubble up and cause the flow to panic.
			if database.IsNotFoundError(err) {
				log.Infof("GetVirtualInfo returned not-found while processing inv %s; skipping inv", inv.Hash)
				continue
			}
			return err
		}
		// We need the PoW hash for processBlock from P2P.
		err = flow.processBlock(inv.Hash, block, false)
		if err != nil {
			missingParentsError := &ruleerrors.ErrMissingParents{}
			if errors.As(err, missingParentsError) {
				if len(missingParentsError.MissingParentHashes) > 0 {
					err := flow.processOrphan(block)
					if err != nil {
						log.Infof("Error processing orphan block %s from %s: %s", inv.Hash, flow.netConnection.Address(), err)
					}
					continue
				}
			} else if errors.Is(err, ruleerrors.ErrDuplicateBlock) {
				continue
			} else if database.IsNotFoundError(err) {
				flow.addToRelayInv(inv.Hash)
				continue
			} else {
				log.Infof("Error processing block %s from %s: %s", inv.Hash, flow.netConnection.Address(), err)
				continue
			}
		}

		oldVirtualParents := hashset.New()
		for _, parent := range oldVirtualInfo.ParentHashes {
			oldVirtualParents.Add(parent)
		}

		newVirtualInfo, err := flow.Domain().Consensus().GetVirtualInfo()
		if err != nil {
			if database.IsNotFoundError(err) {
				log.Infof("GetVirtualInfo returned not-found while processing inv %s (newVirtualInfo); skipping inv", inv.Hash)
				continue
			}
			return err
		}

		virtualHasNewParents := false
		for _, parent := range newVirtualInfo.ParentHashes {
			if oldVirtualParents.Contains(parent) {
				continue
			}
			virtualHasNewParents = true
			block, found, err := flow.Domain().Consensus().GetBlock(parent)
			if err != nil {
				return err
			}

			if !found {
				return protocolerrors.Errorf(false, "Virtual parent %s not found", parent)
			}
			if block.PoWHash != "" {
				blockHash := consensushashing.BlockHash(block)
				log.Debugf("Relaying block %s", blockHash)
				err = flow.relayBlock(block)
				if err != nil {
					return err
				}
			}
		}

		if virtualHasNewParents {
			log.Debugf("Virtual %d has new parents, raising new block template event", newVirtualInfo.DAAScore)
			err = flow.OnNewBlockTemplate()
			if err != nil {
				return err
			}
		}
		txslen := len(block.Transactions)
		k := flow.Config().NetParams().K[constants.GetBlockVersion()-1]
		log.Infof("Accepted block %s from node %s with %d tx, k=%d", inv.Hash, flow.netConnection.Address(), txslen, k)
		err = flow.OnNewBlock(block)
		if err != nil {
			return err
		}
	}
}

func (flow *handleRelayInvsFlow) banIfBlockIsHeaderOnly(block *externalapi.DomainBlock) error {
	if len(block.Transactions) == 0 {
		return protocolerrors.Errorf(true, "sent header of %s block where expected block with body",
			consensushashing.BlockHash(block))
	}

	return nil
}

func (flow *handleRelayInvsFlow) readInv() (invRelayBlock, error) {
	if len(flow.invsQueue) > 0 {
		var inv invRelayBlock
		inv, flow.invsQueue = flow.invsQueue[0], flow.invsQueue[1:]
		return inv, nil
	}

	msg, err := flow.incomingRoute.Dequeue()
	if err != nil {
		return invRelayBlock{}, err
	}

	msgInv, ok := msg.(*appmessage.MsgInvRelayBlock)
	if !ok {
		return invRelayBlock{}, protocolerrors.Errorf(true, "unexpected %s message in the block relay handleRelayInvsFlow while expecting an inv message", msg.Command())
	}
	return invRelayBlock{Hash: msgInv.Hash, IsOrphanRoot: false}, nil
}

func (flow *handleRelayInvsFlow) requestBlock(requestHash *externalapi.DomainHash) (*externalapi.DomainBlock, bool, error) {
	exists := flow.SharedRequestedBlocks().AddIfNotExists(requestHash)
	if exists {
		return nil, true, nil
	}

	// In case the function returns earlier than expected, we want to make sure flow.SharedRequestedBlocks() is
	// clean from any pending blocks.
	defer flow.SharedRequestedBlocks().Remove(requestHash)

	getRelayBlocksMsg := appmessage.NewMsgRequestRelayBlocks([]*externalapi.DomainHash{requestHash})
	err := flow.outgoingRoute.Enqueue(getRelayBlocksMsg)
	if err != nil {
		return nil, false, err
	}

	msgBlock, err := flow.readMsgBlock()
	if err != nil {
		return nil, false, err
	}
	block := appmessage.MsgBlockToDomainBlock(msgBlock)
	blockHash := consensushashing.BlockHash(block)
	if !blockHash.Equal(requestHash) {
		return nil, false, protocolerrors.Errorf(true, "got unrequested block %s", blockHash)
	}

	return block, false, nil
}

// readMsgBlock returns the next msgBlock in msgChan, and populates invsQueue with any inv messages that meanwhile arrive.
//
// Note: this function assumes msgChan can contain only appmessage.MsgInvRelayBlock and appmessage.MsgBlock messages.
func (flow *handleRelayInvsFlow) readMsgBlock() (msgBlock *appmessage.MsgBlock, err error) {
	for {
		message, err := flow.incomingRoute.DequeueWithTimeout(common.DefaultTimeout)
		if err != nil {
			return nil, err
		}

		switch message := message.(type) {
		case *appmessage.MsgInvRelayBlock:
			flow.invsQueue = append(flow.invsQueue, invRelayBlock{Hash: message.Hash, IsOrphanRoot: false})
		case *appmessage.MsgBlock:
			return message, nil
		default:
			return nil, errors.Errorf("unexpected message %s", message.Command())
		}
	}
}

func (flow *handleRelayInvsFlow) addToRelayInv(hash *externalapi.DomainHash) {
	flow.invsQueue = append([]invRelayBlock{{Hash: hash, IsOrphanRoot: false}}, flow.invsQueue...)
	flow.SharedRequestedBlocks().Remove(hash)
	log.Debugf("Re-queued block %s to relay INV queue (missing data in DB)", hash)
}

func (flow *handleRelayInvsFlow) processBlock(hash *externalapi.DomainHash, block *externalapi.DomainBlock, powSkip bool) error {
	err := flow.Domain().Consensus().ValidateAndInsertBlock(block, true, powSkip)
	if err != nil {
		return err
	}
	return nil
}

func (flow *handleRelayInvsFlow) relayBlock(block *externalapi.DomainBlock) error {
	if block.PoWHash == "" && block.Header.Version() >= constants.PoWIntegrityMinVersion {
		return nil
	}
	blockHash := consensushashing.BlockHash(block)
	return flow.Broadcast(appmessage.NewMsgInvBlock(blockHash))
}

func (flow *handleRelayInvsFlow) processOrphan(block *externalapi.DomainBlock) error {
	blockHash := consensushashing.BlockHash(block)

	// Return if the block has been orphaned from elsewhere already
	if flow.IsOrphan(blockHash) {
		log.Debugf("Skipping orphan processing for block %s because it is already an orphan", blockHash)
		return nil
	}

	if block.Header.Version() != constants.GetBlockVersion() {
		log.Debugf("Skipping orphan processing for block %s because it is wrong block version", blockHash)
		return nil
	}

	if block.PoWHash == "" && block.Header.Version() >= constants.PoWIntegrityMinVersion {
		log.Debugf("Skipping orphan processing for block %s because it is missing pow hash", blockHash)
		return nil
	}

	// Add the block to the orphan set if it's within orphan resolution range
	isBlockInOrphanResolutionRange, err := flow.isBlockInOrphanResolutionRange(blockHash)
	if err != nil {
		return err
	}
	if isBlockInOrphanResolutionRange {
		if flow.Config().NetParams().DisallowDirectBlocksOnTopOfGenesis && !flow.Config().AllowSubmitBlockWhenNotSynced {
			isGenesisVirtualSelectedParent, err := flow.isGenesisVirtualSelectedParent()
			if err != nil {
				return err
			}

			if isGenesisVirtualSelectedParent {
				log.Infof("Cannot process orphan %s for a node with only the genesis block. The node needs to IBD to the recent pruning point before normal operation can resume.", blockHash)
				return nil
			}
		}
		flow.AddOrphan(block)
		log.Debugf("Requesting block %s missing ancestors", blockHash)
		return flow.AddOrphanRootsToQueue(blockHash)
	}

	// Start IBD unless we already are in IBD

	// Send the block to IBD flow via the IBDRequestChannel.
	// Note that this is a non-blocking send, since if IBD is already running, there is no need to trigger it
	select {
	case flow.peer.IBDRequestChannel() <- block:
	default:
	}
	return nil
}

func (flow *handleRelayInvsFlow) isGenesisVirtualSelectedParent() (bool, error) {
	virtualSelectedParent, err := flow.Domain().Consensus().GetVirtualSelectedParent()
	if err != nil {
		return false, err
	}

	return virtualSelectedParent.Equal(flow.Config().NetParams().GenesisHash), nil
}

func (flow *handleRelayInvsFlow) isChildOfGenesis(block *externalapi.DomainBlock) bool {
	parents := block.Header.DirectParents()
	return len(parents) == 1 && parents[0].Equal(flow.Config().NetParams().GenesisHash)
}

// isBlockInOrphanResolutionRange finds out whether the given blockHash should be
// retrieved via the unorphaning mechanism or via IBD. This method sends a
// getBlockLocator request to the peer with a limit of orphanResolutionRange.
// In the response, if we know none of the hashes, we should retrieve the given
// blockHash via IBD. Otherwise, via unorphaning.
func (flow *handleRelayInvsFlow) isBlockInOrphanResolutionRange(blockHash *externalapi.DomainHash) (bool, error) {
	err := flow.sendGetBlockLocator(blockHash, orphanResolutionRange)
	if err != nil {
		return false, err
	}

	blockLocatorHashes, err := flow.receiveBlockLocator()
	if err != nil {
		return false, err
	}
	for _, blockLocatorHash := range blockLocatorHashes {
		blockInfo, err := flow.Domain().Consensus().GetBlockInfo(blockLocatorHash)
		if err != nil {
			return false, err
		}
		if blockInfo.Exists && blockInfo.BlockStatus != externalapi.StatusHeaderOnly {
			return true, nil
		}
	}
	return false, nil
}

func (flow *handleRelayInvsFlow) isOrphanRootInQueue(root *externalapi.DomainHash) bool {
	for _, invRelayBlock := range flow.invsQueue {
		if invRelayBlock.Hash.Equal(root) {
			return true
		}
	}
	return false
}

func (flow *handleRelayInvsFlow) AddOrphanRootsToQueue(orphan *externalapi.DomainHash) error {
	orphanRoots, orphanExists, err := flow.GetOrphanRoots(orphan)
	if err != nil {
		return err
	}

	if !orphanExists {
		log.Debugf("Orphan block %s was missing from the orphan pool while requesting for its roots. This "+
			"probably happened because it was randomly evicted immediately after it was added.", orphan)
	}

	if len(orphanRoots) == 0 {
		// In some rare cases we get here when there are no orphan roots already
		return nil
	}
	log.Debugf("Block %s has %d missing ancestors. Adding them to the invs queue...", orphan, len(orphanRoots))

	invMessages := make([]invRelayBlock, 0, len(orphanRoots))
	for _, root := range orphanRoots {
		if flow.isOrphanRootInQueue(root) {
			log.Debugf("Skip adding duplicate missing ancestor %s to the invs queue", root)
			continue
		}
		log.Debugf("Adding missing ancestor %s to the invs queue", root)
		invMessages = append(invMessages, invRelayBlock{Hash: root, IsOrphanRoot: true})
	}

	flow.invsQueue = append(invMessages, flow.invsQueue...)
	return nil
}
