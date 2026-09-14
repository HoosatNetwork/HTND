package blockrelay

import (
	"github.com/HoosatNetwork/HTND/app/appmessage"
	"github.com/HoosatNetwork/HTND/app/protocol/peer"
	"github.com/HoosatNetwork/HTND/app/protocol/protocolerrors"
	"github.com/HoosatNetwork/HTND/domain"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/infrastructure/network/netadapter/router"
)

// HandleIBDBlockLocatorContext is the interface for the context needed for the HandleIBDBlockLocator flow.
type HandleIBDBlockLocatorContext interface {
	Domain() domain.Domain
	IsIBDRunning() bool
}

// HandleIBDBlockLocator listens to appmessage.MsgIBDBlockLocator messages and sends
// the highest known block that's in the selected parent chain of `targetHash` to the
// requesting peer.
func HandleIBDBlockLocator(context HandleIBDBlockLocatorContext, incomingRoute *router.Route,
	outgoingRoute *router.Route, peer *peer.Peer,
) error {
	for {
		message, err := incomingRoute.Dequeue()
		if err != nil {
			return err
		}
		ibdBlockLocatorMessage := message.(*appmessage.MsgIBDBlockLocator)

		targetHash := ibdBlockLocatorMessage.TargetHash
		log.Debugf("Received IBDBlockLocator from %s with targetHash %s", peer, targetHash)
		synced, err := context.Domain().Consensus().IsNearlySynced()
		if err != nil {
			continue
		}
		if context.IsIBDRunning() && !synced {
			log.Debugf("Node is in IBD, responding with not found for targetHash %s", targetHash)
			ibdBlockLocatorHighestHashNotFoundMessage := appmessage.NewMsgIBDBlockLocatorHighestHashNotFound()
			err = outgoingRoute.Enqueue(ibdBlockLocatorHighestHashNotFoundMessage)
			if err != nil {
				return err
			}
			continue
		}

		blockInfo, err := context.Domain().Consensus().GetBlockInfo(targetHash)
		if err != nil {
			return err
		}
		if !blockInfo.HasHeader() {
			return protocolerrors.Errorf(true, "received IBDBlockLocator "+
				"with an unknown targetHash %s", targetHash)
		}

		// The hash count is not limited on the wire, and each hash below costs a full block read and a selected-chain
		// check under the consensus lock, so a peer repeating a known off-chain block made one message cost millions
		// of block reads. Honest locators are logarithmic in the chain length, so only the first
		// MaxBlockLocatorsPerMsg hashes are considered; the message itself is still accepted.
		blockLocatorHashes := ibdBlockLocatorMessage.BlockLocatorHashes
		if len(blockLocatorHashes) > appmessage.MaxBlockLocatorsPerMsg {
			log.Debugf("IBDBlockLocator from %s has %d hashes, considering the first %d", peer,
				len(blockLocatorHashes), appmessage.MaxBlockLocatorsPerMsg)
			blockLocatorHashes = blockLocatorHashes[:appmessage.MaxBlockLocatorsPerMsg]
		}

		foundHighestHashInTheSelectedParentChainOfTargetHash := false
		for _, blockLocatorHash := range blockLocatorHashes {
			block, found, err := context.Domain().Consensus().GetBlock(blockLocatorHash)
			if err != nil {
				return err
			}

			if !found {
				continue
			}

			if block.PoWHash == "" && block.Header.Version() >= constants.PoWIntegrityMinVersion {
				continue
			}

			isBlockLocatorHashInSelectedParentChainOfHighHash, err := context.Domain().Consensus().IsInSelectedParentChainOf(blockLocatorHash, targetHash)
			if err != nil {
				return err
			}
			if !isBlockLocatorHashInSelectedParentChainOfHighHash {
				continue
			}

			foundHighestHashInTheSelectedParentChainOfTargetHash = true
			log.Debugf("Found a known hash %s amongst peer %s's "+
				"blockLocator that's in the selected parent chain of targetHash %s", blockLocatorHash, peer, targetHash)

			ibdBlockLocatorHighestHashMessage := appmessage.NewMsgIBDBlockLocatorHighestHash(blockLocatorHash)
			err = outgoingRoute.Enqueue(ibdBlockLocatorHighestHashMessage)
			if err != nil {
				return err
			}
			break
		}

		if !foundHighestHashInTheSelectedParentChainOfTargetHash {
			log.Warnf("no hash was found in the blockLocator "+
				"that was in the selected parent chain of targetHash %s", targetHash)

			ibdBlockLocatorHighestHashNotFoundMessage := appmessage.NewMsgIBDBlockLocatorHighestHashNotFound()
			err = outgoingRoute.Enqueue(ibdBlockLocatorHighestHashNotFoundMessage)
			if err != nil {
				return err
			}
		}
	}
}
