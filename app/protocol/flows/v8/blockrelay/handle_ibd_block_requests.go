package blockrelay

import (
	"runtime"
	"time"

	"github.com/HoosatNetwork/HTND/app/appmessage"
	peerpkg "github.com/HoosatNetwork/HTND/app/protocol/peer"
	"github.com/HoosatNetwork/HTND/app/protocol/protocolerrors"
	"github.com/HoosatNetwork/HTND/domain"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/infrastructure/network/netadapter/router"
)

// HandleIBDBlockRequestsContext is the interface for the context needed for the HandleIBDBlockRequests flow.
type HandleIBDBlockRequestsContext interface {
	Domain() domain.Domain
}

// HandleIBDBlockRequests listens to appmessage.MsgRequestRelayBlocks messages and sends
// their corresponding blocks to the requesting peer.
func HandleIBDBlockRequests(context HandleIBDBlockRequestsContext, incomingRoute *router.Route,
	outgoingRoute *router.Route, peer *peerpkg.Peer,
) error {
	threadcount := runtime.NumCPU() * 8
	semaphore := make(chan struct{}, threadcount)

	rateLimit := time.NewTicker(time.Second / time.Duration(threadcount))
	defer rateLimit.Stop()
	for {
		<-rateLimit.C // wait for rate limiter
		message, err := incomingRoute.Dequeue()
		if err != nil {
			return err
		}
		msgRequestIBDBlocks := message.(*appmessage.MsgRequestIBDBlocks)
		log.Debugf("Got request for %d ibd blocks", len(msgRequestIBDBlocks.Hashes))

		err = serveRequestedBlocks(msgRequestIBDBlocks.Hashes, semaphore, "HandleIBDBlockRequests-worker",
			func(hash *externalapi.DomainHash) error {
				// Fetch the block from the database.
				block, found, err := context.Domain().Consensus().GetBlock(hash)
				if err != nil {
					return protocolerrors.Errorf(false, "unable to fetch requested IBD block %s: %s", hash, err)
				}

				if !found {
					block, err = context.Domain().Consensus().GetBlockEvenIfHeaderOnly(hash)
					if err != nil {
						return protocolerrors.Errorf(false, "requested IBD block %s not found: %s", hash, err)
					}
				}

				// TODO (Partial nodes): Convert block to partial block if needed
				log.Debugf("Relaying IBD block %s to peer %s", hash, peer.Address())
				ibdBlockMessage := appmessage.NewMsgIBDBlock(appmessage.DomainBlockToMsgBlock(block))
				return outgoingRoute.Enqueue(ibdBlockMessage)
			})
		if err != nil {
			return err
		}
	}
}
